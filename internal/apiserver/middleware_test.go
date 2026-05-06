package apiserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kborup-redhat/ovro/internal/apiserver"
	"github.com/stretchr/testify/assert"
	authenticationv1 "k8s.io/api/authentication/v1"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeK8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newFakeClientset(authenticated bool, allowed bool) *fakeK8s.Clientset {
	cs := fakeK8s.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status = authenticationv1.TokenReviewStatus{
			Authenticated: authenticated,
			User: authenticationv1.UserInfo{
				Username: "test-user",
				UID:      "uid-123",
				Groups:   []string{"system:authenticated"},
			},
		}
		return true, tr, nil
	})
	cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		sar.Status = authv1.SubjectAccessReviewStatus{Allowed: allowed}
		return true, sar, nil
	})
	return cs
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	handler := apiserver.AuthMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_NilClientset(t *testing.T) {
	handler := apiserver.AuthMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	cs := newFakeClientset(false, false)
	handler := apiserver.AuthMiddleware(cs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_Forbidden(t *testing.T) {
	cs := newFakeClientset(true, false)
	handler := apiserver.AuthMiddleware(cs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/recommendations/default/test/apply", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAuthMiddleware_Allowed(t *testing.T) {
	cs := newFakeClientset(true, true)
	handler := apiserver.AuthMiddleware(cs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_GETRequiresAuth(t *testing.T) {
	cs := newFakeClientset(true, false)
	handler := apiserver.AuthMiddleware(cs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestAuthMiddleware_UserInfoInContext(t *testing.T) {
	cs := newFakeClientset(true, true)
	var userInfo *apiserver.UserInfo
	handler := apiserver.AuthMiddleware(cs)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userInfo = apiserver.UserInfoFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/recommendations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotNil(t, userInfo)
	assert.Equal(t, "test-user", userInfo.Username)
	assert.Equal(t, "uid-123", userInfo.UID)
}
