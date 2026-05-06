---
title: "Chapter 9: REST API Server"
order: 9
---

# Chapter 9: REST API Server

## Introduction

The REST API Server bridges the Kubernetes world and the frontend. It serves as the backend for the OpenShift Console plugin, handling listing, applying, reverting, and excluding recommendations, plus policy management. Every request goes through RBAC-enforced authentication and namespace-scoped authorisation. Think of it as a secure translation layer: the frontend speaks REST, the backend speaks Kubernetes API.

## Server Structure

```go
// internal/apiserver/server.go

type Server struct {
    K8sClient         client.Client
    Clientset         kubernetes.Interface
    DynamicClient     dynamic.Interface
    OwnerResolver     *owner.Resolver
    TokenManager      *approval.TokenManager
    Notifier          *notifier.Dispatcher
    ApprovalRouteHost string
    DemoMode          bool
    mux               *http.ServeMux
}
```

The server uses Go 1.22's enhanced `http.ServeMux` with method-based routing:

```go
func (s *Server) registerRoutes() {
    auth := AuthMiddleware(s.Clientset)

    s.mux.Handle("GET /api/v1/recommendations", auth(http.HandlerFunc(s.handleListRecommendations)))
    s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/apply", auth(http.HandlerFunc(s.handleApply)))
    s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/revert", auth(http.HandlerFunc(s.handleRevert)))
    s.mux.Handle("GET /api/v1/vms", auth(http.HandlerFunc(s.handleListVMs)))
    s.mux.Handle("POST /api/v1/vms/{namespace}/{name}/exclude", auth(http.HandlerFunc(s.handleExclude)))
    s.mux.Handle("GET /api/v1/overview", auth(http.HandlerFunc(s.handleOverview)))
    s.mux.Handle("GET /api/v1/policy", auth(http.HandlerFunc(s.handleGetPolicy)))
    s.mux.Handle("PUT /api/v1/policy", auth(http.HandlerFunc(s.handleUpdatePolicy)))

    // Internal endpoints (no auth — approval proxy validates JWT)
    s.mux.Handle("POST /api/v1/internal/recommendations/{namespace}/{name}/owner-approve",
        http.HandlerFunc(s.handleOwnerApprove))
    s.mux.Handle("POST /api/v1/internal/recommendations/{namespace}/{name}/owner-reject",
        http.HandlerFunc(s.handleOwnerReject))
}
```

## Authentication & Authorisation

Every external request goes through the `AuthMiddleware`:

```go
// internal/apiserver/middleware.go

func AuthMiddleware(clientset kubernetes.Interface) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 1. Extract bearer token
            token := extractBearerToken(r)

            // 2. TokenReview — is this a valid OpenShift token?
            tr, _ := clientset.AuthenticationV1().TokenReviews().Create(...)
            if !tr.Status.Authenticated { return 401 }

            // 3. SubjectAccessReview — can this user perform this action?
            sar := &authv1.SubjectAccessReview{
                Spec: authv1.SubjectAccessReviewSpec{
                    User:   user.Username,
                    Groups: user.Groups,
                    ResourceAttributes: &authv1.ResourceAttributes{
                        Verb:      verb,     // derived from HTTP method
                        Group:     "rightsizing.redhatconsulting.io",
                        Resource:  resource,  // derived from URL path
                        Namespace: namespace, // derived from URL path
                    },
                },
            }
            result, _ := clientset.AuthorizationV1().SubjectAccessReviews().Create(...)
            if !result.Status.Allowed { return 403 }

            // 4. Continue with user info in context
            ctx := context.WithValue(r.Context(), userInfoKey, user)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

This two-step process ensures:
1. **Authentication** — the token is valid (TokenReview)
2. **Authorisation** — the user has permission for this specific action (SubjectAccessReview)

The console plugin forwards the user's OpenShift token via the `consoleFetch` SDK function, which automatically includes the bearer token.

## Namespace Scoping

List endpoints filter results by namespace access:

```go
func (s *Server) filterByNamespaceAccess(r *http.Request, items []RightsizingRecommendation) []RightsizingRecommendation {
    checked := make(map[string]bool) // cache SAR results
    var filtered []RightsizingRecommendation
    for _, item := range items {
        ns := item.Namespace
        allowed, exists := checked[ns]
        if !exists {
            allowed = s.canUserAccessNamespace(r, user, ns, "get")
            checked[ns] = allowed
        }
        if allowed {
            filtered = append(filtered, item)
        }
    }
    return filtered
}
```

The namespace check cache prevents redundant SubjectAccessReviews for VMs in the same namespace.

## The Apply Handler

The most complex handler manages the apply flow:

```go
func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
    // 1. Parse request body (restart option, scheduled time)
    // 2. Fetch recommendation, verify state is "pending"

    // 3. Demo mode: always generate approval token
    if s.DemoMode && s.TokenManager != nil {
        // Generate JWT, set state to awaiting-approval
        // Return 202 with approval URL
        return
    }

    // 4. Check for owner — route to approval if found
    if s.OwnerResolver != nil && s.TokenManager != nil && s.Notifier != nil {
        ownerStr, _ := s.OwnerResolver.ResolveOwner(...)
        if ownerStr != "" {
            // Generate JWT, send notifications, return 202
            return
        }
    }

    // 5. No owner — apply directly
    // Set state to applied (hotplug) or applied-pending-restart
    // Return 200 with updated recommendation
}
```

The 202 response for approval includes the `approvalUrl` so the frontend can display it.

## Functional Options

The server uses the functional options pattern for backward-compatible configuration:

```go
type ServerOption func(*Server)

func WithOwnerResolver(r *owner.Resolver) ServerOption {
    return func(s *Server) { s.OwnerResolver = r }
}
func WithTokenManager(tm *approval.TokenManager) ServerOption {
    return func(s *Server) { s.TokenManager = tm }
}
func WithDemoMode(enabled bool) ServerOption {
    return func(s *Server) { s.DemoMode = enabled }
}
```

This allows the `main.go` entrypoint to configure the server progressively: approval components are only added when their prerequisites are available.

## Key Takeaways

- Two-layer RBAC: TokenReview (authn) + SubjectAccessReview (authz) on every request.
- Namespace-scoped filtering ensures users only see data they're authorised for.
- The apply handler has three paths: demo mode, approval workflow, and direct apply.
- Internal endpoints bypass auth — the approval proxy handles its own JWT validation.
- Functional options allow progressive feature enablement without breaking the constructor.

## Next Steps

The API server provides data; the Console Plugin presents it. Let's look at how the React frontend brings everything together in the OpenShift Console.
