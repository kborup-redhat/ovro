package apiserver

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type UserInfo struct {
	Username string
	UID      string
	Groups   []string
}

type contextKey string

const userInfoKey contextKey = "userInfo"

func UserInfoFromContext(ctx context.Context) *UserInfo {
	if info, ok := ctx.Value(userInfoKey).(*UserInfo); ok {
		return info
	}
	return nil
}

func AuthMiddleware(clientset kubernetes.Interface) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if clientset == nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			tokenReview := &authenticationv1.TokenReview{
				Spec: authenticationv1.TokenReviewSpec{Token: token},
			}
			tr, err := clientset.AuthenticationV1().TokenReviews().Create(
				r.Context(), tokenReview, metav1.CreateOptions{},
			)
			if err != nil {
				slog.Error("token review failed", "error", err)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if !tr.Status.Authenticated {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			user := &UserInfo{
				Username: tr.Status.User.Username,
				UID:      tr.Status.User.UID,
				Groups:   tr.Status.User.Groups,
			}

			verb := httpMethodToVerb(r.Method)
			namespace := extractNamespaceFromPath(r.URL.Path)
			resource := resourceFromPath(r.URL.Path)

			sar := &authv1.SubjectAccessReview{
				Spec: authv1.SubjectAccessReviewSpec{
					User:   user.Username,
					Groups: user.Groups,
					ResourceAttributes: &authv1.ResourceAttributes{
						Verb:      verb,
						Group:     "rightsizing.redhatconsulting.io",
						Resource:  resource,
						Namespace: namespace,
					},
				},
			}

			result, err := clientset.AuthorizationV1().SubjectAccessReviews().Create(
				r.Context(), sar, metav1.CreateOptions{},
			)
			if err != nil {
				slog.Error("subject access review failed", "error", err)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if !result.Status.Allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			ctx := context.WithValue(r.Context(), userInfoKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}

func httpMethodToVerb(method string) string {
	switch method {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "update"
	case http.MethodPut:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return "get"
	}
}

func extractNamespaceFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// /api/v1/recommendations/{namespace}/{name}[/action]
	// /api/v1/vms/{namespace}/{name}/exclude
	// parts: [api, v1, recommendations|vms|overview|policy, namespace?, name?, action?]
	if len(parts) >= 5 && (parts[2] == "recommendations" || parts[2] == "vms") {
		return parts[3]
	}
	return ""
}

func resourceFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 3 {
		switch parts[2] {
		case "policy":
			return "rightsizingpolicies"
		case "vms":
			return "virtualmachines"
		}
	}
	return "rightsizingrecommendations"
}
