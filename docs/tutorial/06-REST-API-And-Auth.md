---
title: "Chapter 6: REST API and Authentication"
order: 6
---

# Chapter 6: REST API and Authentication

In the previous chapters we built the controllers that watch VirtualMachines, query Prometheus, calculate rightsizing recommendations, and persist them as Custom Resources. But those CRs only live inside the Kubernetes API -- the Console Plugin (coming in Chapter 7) needs a friendlier HTTP interface to read and act on them. That is what the REST API server provides.

More importantly, we cannot just expose cluster data to anyone who can reach the endpoint. Every request must be authenticated and authorised against the caller's actual OpenShift permissions. This chapter covers both sides: the HTTP layer and the security layer.

---

## The Hospital Front Desk Analogy

Think of the API server as a hospital front desk. When you walk in, the receptionist first checks your photo ID to confirm you are who you claim to be -- that is **TokenReview**. Then they look up whether you have an appointment in cardiology or whether you are even allowed in that department -- that is **SubjectAccessReview (SAR)**. Only after both checks pass are you directed to the right service. If your ID is fake or you are not cleared for that department, you are politely turned away at the desk, never reaching the doctor.

This two-step process -- verify identity, then verify permissions -- is exactly how the middleware protects every route.

---

## 1. The Server (`internal/apiserver/server.go`)

The `Server` struct is the entry point. It holds three Kubernetes clients and an HTTP multiplexer:

```go
type Server struct {
    K8sClient     client.Client        // controller-runtime typed client (for CRs)
    Clientset     kubernetes.Interface  // client-go clientset (for TokenReview, SAR)
    DynamicClient dynamic.Interface     // untyped client (for patching VMs)
    mux           *http.ServeMux
}
```

Why three clients? Each serves a different purpose:

- **K8sClient** (controller-runtime) gives us typed access to `RightsizingRecommendation` and `RightsizingPolicy` CRs. It is the same client style the controllers use.
- **Clientset** (client-go) is needed for the `AuthenticationV1` and `AuthorizationV1` sub-clients that create `TokenReview` and `SubjectAccessReview` objects.
- **DynamicClient** lets us patch arbitrary resources -- specifically KubeVirt `VirtualMachine` objects -- without importing the full KubeVirt Go types. We just need the GVR (Group-Version-Resource):

```go
var vmGVR = schema.GroupVersionResource{
    Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines",
}
```

### Constructor and Route Registration

The `NewServer` function wires everything together:

```go
func NewServer(k8sClient client.Client, clientset kubernetes.Interface,
    dynamicClient dynamic.Interface) *Server {
    s := &Server{
        K8sClient:     k8sClient,
        Clientset:     clientset,
        DynamicClient: dynamicClient,
        mux:           http.NewServeMux(),
    }
    s.registerRoutes()
    return s
}
```

The interesting part is `registerRoutes`. Go 1.22 introduced method-based routing in `http.ServeMux`, so we can write `"GET /api/v1/recommendations"` directly instead of checking `r.Method` inside each handler. Every route is wrapped in `auth()`, the authentication middleware:

```go
func (s *Server) registerRoutes() {
    auth := AuthMiddleware(s.Clientset)

    s.mux.Handle("GET /api/v1/recommendations",
        auth(http.HandlerFunc(s.handleListRecommendations)))
    s.mux.Handle("GET /api/v1/recommendations/{namespace}/{name}",
        auth(http.HandlerFunc(s.handleGetRecommendation)))
    s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/apply",
        auth(http.HandlerFunc(s.handleApply)))
    s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/revert",
        auth(http.HandlerFunc(s.handleRevert)))
    s.mux.Handle("POST /api/v1/vms/{namespace}/{name}/exclude",
        auth(http.HandlerFunc(s.handleExclude)))
    s.mux.Handle("DELETE /api/v1/vms/{namespace}/{name}/exclude",
        auth(http.HandlerFunc(s.handleRemoveExclusion)))
    s.mux.Handle("GET /api/v1/overview",
        auth(http.HandlerFunc(s.handleOverview)))
    s.mux.Handle("GET /api/v1/policy",
        auth(http.HandlerFunc(s.handleGetPolicy)))
    s.mux.Handle("PUT /api/v1/policy",
        auth(http.HandlerFunc(s.handleUpdatePolicy)))
}
```

Notice the pattern: every single route goes through `auth(...)`. There is no "public" endpoint. This is deliberate -- the API exposes cluster resources, so every caller must prove their identity and permissions.

The route table maps cleanly to the operations the Console Plugin needs:

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/recommendations` | List all recommendations (with optional filters) |
| GET | `/api/v1/recommendations/{namespace}/{name}` | Get a single recommendation |
| POST | `.../apply` | Apply a recommendation |
| POST | `.../revert` | Revert a previously applied recommendation |
| POST | `/api/v1/vms/{namespace}/{name}/exclude` | Exclude a VM from analysis |
| DELETE | `.../exclude` | Resume monitoring a previously excluded VM |
| GET | `/api/v1/overview` | Dashboard summary data |
| GET | `/api/v1/policy` | Read the current policy |
| PUT | `/api/v1/policy` | Update the policy |

### TLS Hardening

The server supports both plain HTTP and TLS. The `StartTLS` method applies deliberate security hardening:

```go
func (s *Server) StartTLS(addr, certFile, keyFile string) error {
    srv := &http.Server{
        Addr:    addr,
        Handler: s,
        TLSConfig: &tls.Config{
            MinVersion: tls.VersionTLS12,
            NextProtos: []string{"http/1.1"},
        },
    }
    return srv.ListenAndServeTLS(certFile, keyFile)
}
```

Two details worth noting:

1. **MinVersion TLS 1.2** -- TLS 1.0 and 1.1 are known to have vulnerabilities. Setting the minimum to 1.2 ensures connections use modern cipher suites.
2. **`NextProtos: []string{"http/1.1"}`** -- This explicitly disables HTTP/2. Why? HTTP/2 multiplexes many logical streams over a single TCP connection. In certain proxy and operator scenarios this can complicate debugging and introduces stream-reset attacks (CVE-2023-44487, the "rapid reset" attack). Sticking to HTTP/1.1 keeps things simple and avoids that attack surface entirely.

---

## 2. Authentication Middleware (`internal/apiserver/middleware.go`)

This file contains the security logic. It is the "front desk" from our analogy.

### UserInfo and Context

First, the middleware defines a `UserInfo` struct and a typed context key:

```go
type UserInfo struct {
    Username string
    UID      string
    Groups   []string
}

type contextKey string
const userInfoKey contextKey = "userInfo"
```

The `contextKey` type is a common Go pattern. Using a custom type instead of a plain `string` prevents collisions -- no other package can accidentally overwrite our context value because they would need our unexported type.

The `UserInfoFromContext` helper lets handlers retrieve the authenticated user later:

```go
func UserInfoFromContext(ctx context.Context) *UserInfo {
    if info, ok := ctx.Value(userInfoKey).(*UserInfo); ok {
        return info
    }
    return nil
}
```

### The AuthMiddleware Flow

`AuthMiddleware` returns a middleware function -- a function that takes an `http.Handler` and returns a new `http.Handler` that wraps it with security checks. This is the standard Go middleware pattern.

The flow has four steps:

```
Request arrives
    |
    v
[1] Extract bearer token from Authorization header
    |-- missing? --> 401 Unauthorized
    v
[2] TokenReview: validate the token with Kubernetes
    |-- not authenticated? --> 401 Unauthorized
    v
[3] SubjectAccessReview: check if the user can perform this verb on this resource
    |-- not allowed? --> 403 Forbidden
    v
[4] Store UserInfo in request context, pass to handler
```

Let us walk through each step in the code:

**Step 1: Extract the bearer token**

```go
token := extractBearerToken(r)
if token == "" {
    http.Error(w, "unauthorized", http.StatusUnauthorized)
    return
}
```

The `extractBearerToken` helper parses the `Authorization: Bearer <token>` header:

```go
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
```

The `strings.EqualFold` comparison makes the "Bearer" prefix case-insensitive, which matches the HTTP specification.

**Step 2: TokenReview -- "Who are you?"**

```go
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
```

A `TokenReview` is a Kubernetes API object. We send the bearer token to the API server, which validates it (checking expiry, signature, revocation) and returns the user's identity: username, UID, and group memberships. This is the same mechanism the Kubernetes API server itself uses.

Notice the error handling: when the TokenReview fails, we log the real error server-side (`slog.Error`) but return only `"unauthorized"` to the caller. This is intentional -- we never leak internal details like "token validation connection to kube-apiserver timed out" to the client, because that information could help an attacker map the infrastructure.

**Step 3: SubjectAccessReview -- "Are you allowed to do this?"**

```go
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
```

This is where OVRO delegates authorisation decisions to Kubernetes RBAC rather than implementing its own permission system.

**Why not just check group membership?** We could hard-code rules like "if user is in group `cluster-admins`, allow everything." But that would create a shadow permission system that drifts from the cluster's actual RBAC configuration. By using `SubjectAccessReview`, we ask Kubernetes: "given this user, their groups, and all the `ClusterRole`, `Role`, `ClusterRoleBinding`, and `RoleBinding` objects in the cluster, is this action allowed?" The answer reflects the real RBAC state, including any custom roles an administrator has created.

The helper functions translate HTTP concepts into Kubernetes RBAC concepts:

```go
func httpMethodToVerb(method string) string {
    switch method {
    case http.MethodGet:    return "get"
    case http.MethodPost:   return "update"
    case http.MethodPut:    return "update"
    case http.MethodDelete: return "delete"
    default:                return "get"
    }
}
```

Notice that `POST` maps to `"update"` rather than `"create"`. This is because POST routes in our API (like `/apply` and `/revert`) modify existing resources rather than creating new ones.

```go
func extractNamespaceFromPath(path string) string {
    parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
    // /api/v1/recommendations/{namespace}/{name}[/action]
    // /api/v1/vms/{namespace}/{name}/exclude
    if len(parts) >= 5 && (parts[2] == "recommendations" || parts[2] == "vms") {
        return parts[3]
    }
    return ""
}
```

For cluster-scoped routes like `/api/v1/policy` or `/api/v1/overview`, the namespace comes back empty, meaning the SAR checks cluster-wide permissions.

```go
func resourceFromPath(path string) string {
    parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
    if len(parts) >= 3 {
        switch parts[2] {
        case "policy": return "rightsizingpolicies"
        case "vms":    return "virtualmachines"
        }
    }
    return "rightsizingrecommendations"
}
```

This maps URL segments to the actual CRD resource names that RBAC rules reference.

**Step 4: Store UserInfo in context**

```go
ctx := context.WithValue(r.Context(), userInfoKey, user)
next.ServeHTTP(w, r.WithContext(ctx))
```

After both checks pass, the authenticated user's information is attached to the request context and the request proceeds to the actual handler. Any handler downstream can call `UserInfoFromContext(r.Context())` to find out who the caller is.

### The ConsolePlugin Proxy Pattern

You might wonder: where does the bearer token come from in the first place? The Console Plugin running in the user's browser does not have direct access to the API server. Instead, the `ConsolePlugin` Custom Resource declares a proxy with `authorize: true`:

```yaml
spec:
  proxy:
    - alias: ovro-api
      endpoint:
        type: Service
        service:
          name: ovro-api
          namespace: ovro-system
          port: 9443
      authorize: true    # <-- the key setting
```

When `authorize: true` is set, the OpenShift Console automatically injects the logged-in user's bearer token into the `Authorization` header of every proxied request. The user never sees or handles the token directly. The Console acts as a transparent relay, and our middleware validates the forwarded token through the normal TokenReview/SAR flow. This means the Console Plugin inherits the same RBAC boundaries as the user -- a namespace-scoped developer sees only their namespaces, while a cluster-admin sees everything.

---

## 3. Request Handlers (`internal/apiserver/handlers.go`)

With authentication and authorisation handled by the middleware, the handlers focus on business logic. Let us examine the key patterns.

### The `writeJSON` Helper

Every handler that returns data uses this helper:

```go
func writeJSON(w http.ResponseWriter, v interface{}) {
    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(v); err != nil {
        slog.Error("encoding JSON response", "error", err)
    }
}
```

A small but important pattern: if JSON encoding fails (which is rare but possible with certain types), the error is logged server-side but not sent to the client. The response headers have already been written at that point, so there is no clean way to change the status code -- logging is the best we can do.

### Namespace Filtering: List All, Then Filter by RBAC

The `handleListRecommendations` handler demonstrates a pattern that appears several times:

```go
func (s *Server) handleListRecommendations(w http.ResponseWriter, r *http.Request) {
    list := &rightsizingv1alpha1.RightsizingRecommendationList{}
    opts := []client.ListOption{}

    if ns := r.URL.Query().Get("namespace"); ns != "" {
        opts = append(opts, client.InNamespace(ns))
    }

    if err := s.K8sClient.List(r.Context(), list, opts...); err != nil {
        slog.Error("listing recommendations", "error", err)
        http.Error(w, "internal server error", http.StatusInternalServerError)
        return
    }

    items := s.filterByNamespaceAccess(r, list.Items)
    // ... apply direction and state filters ...
}
```

The pattern is: **list everything the operator can see, then filter down to what the user is allowed to see.** The operator's service account has cluster-wide permissions to read all `RightsizingRecommendation` CRs, but the calling user might only have access to certain namespaces.

The `filterByNamespaceAccess` function does the per-namespace check with a **caching optimisation**:

```go
func (s *Server) filterByNamespaceAccess(r *http.Request, 
    items []rightsizingv1alpha1.RightsizingRecommendation) []rightsizingv1alpha1.RightsizingRecommendation {
    user := UserInfoFromContext(r.Context())
    if user == nil {
        return nil
    }

    checked := make(map[string]bool)
    var filtered []rightsizingv1alpha1.RightsizingRecommendation
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

The `checked` map caches the SAR result per namespace for the duration of a single request. If there are 50 recommendations across 3 namespaces, this issues only 3 SubjectAccessReview calls instead of 50. Each `canUserAccessNamespace` call creates a fresh SAR:

```go
func (s *Server) canUserAccessNamespace(r *http.Request, user *UserInfo,
    namespace, verb string) bool {
    sar := &authv1.SubjectAccessReview{
        Spec: authv1.SubjectAccessReviewSpec{
            User:   user.Username,
            Groups: user.Groups,
            ResourceAttributes: &authv1.ResourceAttributes{
                Verb:      verb,
                Group:     "rightsizing.redhatconsulting.io",
                Resource:  "rightsizingrecommendations",
                Namespace: namespace,
            },
        },
    }
    result, err := s.Clientset.AuthorizationV1().SubjectAccessReviews().Create(
        r.Context(), sar, metav1.CreateOptions{},
    )
    if err != nil {
        slog.Error("namespace access check failed", "namespace", namespace, "error", err)
        return false
    }
    return result.Status.Allowed
}
```

Notice the fail-closed behaviour: if the SAR call itself fails (network error, API server down), the function returns `false`. The user is denied access rather than accidentally granted it.

After namespace filtering, the handler applies optional query-string filters for `direction` (downsize/upsize) and `state` (pending/applied/etc.):

```go
if direction := r.URL.Query().Get("direction"); direction != "" {
    filtered := make([]rightsizingv1alpha1.RightsizingRecommendation, 0)
    for _, item := range items {
        if string(item.Spec.Direction) == direction {
            filtered = append(filtered, item)
        }
    }
    items = filtered
}
```

### Applying a Recommendation (`handleApply`)

This is the most complex handler because it involves state validation, body parsing, and branching logic based on VM capabilities.

**Body size limiting with MaxBytesReader:**

```go
r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
var req ApplyRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
    http.Error(w, "invalid request body", http.StatusBadRequest)
    return
}
```

`MaxBytesReader` wraps the request body and enforces a 1 MB limit (`maxBodySize = 1 << 20`). If a client sends a 10 GB request body -- whether by accident or as a denial-of-service attack -- the server stops reading after 1 MB and returns an error. Without this protection, a malicious client could exhaust the server's memory by sending an arbitrarily large payload. This is a standard defence against memory exhaustion attacks.

**State validation:**

```go
if rec.Status.State != rightsizingv1alpha1.StatePending {
    http.Error(w, "recommendation is not in pending state", http.StatusConflict)
    return
}
```

A recommendation can only be applied if it is in the `pending` state. If it has already been applied, reverted, or is in any other state, the handler returns a `409 Conflict`. This prevents double-application and enforces the state machine.

**Hotplug-aware state transitions:**

```go
if rec.Spec.HotplugCapable {
    rec.Status.State = rightsizingv1alpha1.StateApplied
    rec.Status.AppliedAt = &now
} else {
    rec.Status.State = rightsizingv1alpha1.StateAppliedPendingRestart
    switch req.RestartOption {
    case "schedule":
        t, err := time.Parse(time.RFC3339, req.ScheduledAt)
        if err != nil {
            http.Error(w, "invalid scheduledAt format (use RFC3339)", http.StatusBadRequest)
            return
        }
        mt := metav1.NewTime(t)
        rec.Status.ScheduledRestartAt = &mt
    case "now":
        rec.Status.AppliedAt = &now
    case "later":
        // No action -- user will restart manually
    }
}
```

If the VM supports CPU/memory hotplug, the change takes effect immediately and the state goes straight to `applied`. If hotplug is not available, the VM needs a restart, so the state becomes `applied-pending-restart` and the user can choose:

- **`"schedule"`** -- schedule a restart at a specific time (parsed as RFC3339). The restart controller will pick this up.
- **`"now"`** -- restart immediately.
- **`"later"`** -- leave it pending; the user will restart manually when convenient.

Before the state change, the handler saves the current resource spec as `RevertConfig` so the change can be undone later:

```go
rec.Status.RevertConfig = &rightsizingv1alpha1.ResourceSpec{
    CPU:    rec.Spec.Current.CPU,
    Memory: rec.Spec.Current.Memory,
}
```

### Reverting a Recommendation (`handleRevert`)

Reverting checks that the recommendation is in a state that can actually be reverted:

```go
if rec.Status.State != rightsizingv1alpha1.StateApplied &&
    rec.Status.State != rightsizingv1alpha1.StateAppliedPendingRestart {
    http.Error(w, "recommendation cannot be reverted in current state", http.StatusConflict)
    return
}

if rec.Status.RevertConfig == nil {
    http.Error(w, "no revert config available", http.StatusConflict)
    return
}

rec.Status.State = rightsizingv1alpha1.StateReverted
```

Only `applied` and `applied-pending-restart` recommendations can be reverted. The state machine prevents reverting something that was never applied or has already been reverted.

### Excluding and Re-including VMs (`handleExclude`, `handleRemoveExclusion`)

These handlers use the dynamic client to patch VM annotations without needing KubeVirt Go types:

```go
// Exclude
patch := []byte(`{"metadata":{"annotations":{
    "rightsizing.redhatconsulting.io/exclude":"true"}}}`)
_, err := s.DynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
    r.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{},
)
```

```go
// Remove exclusion
patch := []byte(`{"metadata":{"annotations":{
    "rightsizing.redhatconsulting.io/exclude":null}}}`)
```

Setting the annotation value to `null` in a merge patch removes the annotation entirely, rather than setting it to an empty string. The Recommendation Controller checks for this annotation and skips any VM that has it.

### Updating the Policy (`handleUpdatePolicy`)

This handler demonstrates comprehensive input validation:

```go
if policySpec.LookbackDays <= 0 || policySpec.LookbackDays > 365 {
    http.Error(w, "lookbackDays must be between 1 and 365", http.StatusBadRequest)
    return
}
if policySpec.Algorithm.Percentile < 1 || policySpec.Algorithm.Percentile > 100 {
    http.Error(w, "percentile must be between 1 and 100", http.StatusBadRequest)
    return
}
if policySpec.Algorithm.HeadroomPercent < 0 {
    http.Error(w, "headroomPercent must be non-negative", http.StatusBadRequest)
    return
}
if policySpec.Thresholds.MinCPUSavings < 0 {
    http.Error(w, "minCpuSavings must be non-negative", http.StatusBadRequest)
    return
}
if policySpec.Thresholds.UpsizeUtilizationPercent < 1 ||
    policySpec.Thresholds.UpsizeUtilizationPercent > 100 {
    http.Error(w, "upsizeUtilizationPercent must be between 1 and 100", http.StatusBadRequest)
    return
}
if policySpec.ReconcileIntervalMinutes <= 0 {
    http.Error(w, "reconcileIntervalMinutes must be greater than 0", http.StatusBadRequest)
    return
}
if policySpec.RevertRetentionDays <= 0 {
    http.Error(w, "revertRetentionDays must be greater than 0", http.StatusBadRequest)
    return
}
```

Every field is validated at the API boundary before the policy is written back to the cluster. This is defence in depth -- even though the CRD schema might also have validation, the API server catches bad input early and returns clear error messages, preventing invalid configurations from ever reaching the controller.

---

## Security Patterns Summary

Several deliberate security choices appear throughout these three files. Here they are collected in one place:

| Pattern | Where | Why |
|---------|-------|-----|
| Generic error messages to clients | All error responses | Prevents information leakage -- attackers cannot probe internal architecture |
| Detailed server-side logging | `slog.Error` calls | Operators and SREs can diagnose issues without exposing details to callers |
| `MaxBytesReader` on POST/PUT bodies | `handleApply`, `handleUpdatePolicy` | Prevents memory exhaustion from oversized request bodies |
| TokenReview + SAR (not group checks) | `AuthMiddleware` | Delegates auth to Kubernetes RBAC -- respects existing cluster policies |
| Fail-closed on errors | `canUserAccessNamespace` | If the SAR call fails, access is denied rather than accidentally granted |
| TLS 1.2 minimum | `StartTLS` | Excludes broken older protocol versions |
| HTTP/2 disabled | `StartTLS` | Eliminates stream-reset DoS attack surface |
| Input validation at API boundary | `handleUpdatePolicy` | Bad data is rejected before it reaches the cluster |

---

## Putting It All Together

Here is the complete request flow, from the user clicking "Apply" in the Console Plugin to the response appearing in their browser:

```
User clicks "Apply" in Console Plugin
    |
    v
Console injects bearer token (authorize: true in ConsolePlugin CR)
    |
    v
POST /api/v1/recommendations/my-ns/my-vm/apply
    |
    v
AuthMiddleware:
    1. extractBearerToken --> gets the token
    2. TokenReview --> Kubernetes confirms: "This is user kim@example.com"
    3. httpMethodToVerb(POST) --> "update"
       extractNamespaceFromPath --> "my-ns"
       resourceFromPath --> "rightsizingrecommendations"
    4. SubjectAccessReview --> Kubernetes confirms: "kim can update
       rightsizingrecommendations in my-ns"
    5. Store UserInfo in context
    |
    v
handleApply:
    1. MaxBytesReader limits body to 1 MB
    2. Decode ApplyRequest (restartOption, scheduledAt)
    3. Fetch the RightsizingRecommendation CR
    4. Validate state == "pending"
    5. Save current resources as RevertConfig
    6. Set new state based on hotplug capability
    7. Update CR status
    8. Return updated CR as JSON
```

---

## Key Takeaways

- **The API server is a thin RBAC-enforced gateway** over Kubernetes Custom Resources. It translates HTTP verbs and paths into Kubernetes authorisation checks, then performs the actual operations using the controller-runtime client.

- **TokenReview + SubjectAccessReview delegates all permission decisions to Kubernetes RBAC.** This means cluster administrators manage OVRO permissions the same way they manage everything else -- through Roles and RoleBindings. There is no separate permission database to maintain.

- **The ConsolePlugin proxy pattern** (`authorize: true`) transparently forwards the user's bearer token, so the API server always knows who the real caller is without the Console Plugin needing to handle tokens itself.

- **Namespace filtering (list all, then filter by SAR)** lets the operator use its cluster-wide access to fetch data efficiently while still respecting per-user namespace boundaries. The per-request namespace cache prevents redundant SAR calls.

- **Security is layered**: TLS hardening at the transport layer, token validation at the identity layer, SAR checks at the authorisation layer, MaxBytesReader at the input layer, and generic error messages at the output layer.

---

## Next Chapter Preview

With the API server and authentication in place, we have a secure backend ready to serve data. In [Chapter 7: Console Plugin](07-Console-Plugin.md), we will build the React/TypeScript frontend that the user actually sees -- an OpenShift Console dynamic plugin with overview dashboards, recommendation tables, exclusion management, and policy configuration, all powered by the REST API we just covered.
