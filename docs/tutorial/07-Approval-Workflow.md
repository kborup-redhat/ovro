---
title: "Chapter 7: Approval Workflow"
order: 7
---

# Chapter 7: Approval Workflow

## Introduction

Not every rightsizing change should be applied immediately. When a VM has an owner, OVRO routes the change through an approval workflow: the owner receives a notification with a signed link, reviews the recommendation on a standalone web page, and approves or rejects it. This chapter covers three components that make this possible: the **Token Manager**, the **Owner Resolver**, and the **Approval Proxy**.

## Token Manager

The Token Manager generates and validates JWT (JSON Web Token) approval tokens signed with HMAC-SHA256:

```go
// internal/approval/token.go

type ApprovalClaims struct {
    jwt.RegisteredClaims
    Namespace string `json:"ns"`
    RecName   string `json:"rec"`
    Owner     string `json:"owner"`
}

type TokenManager struct {
    signingKey []byte
}
```

### Generating Tokens

```go
func (tm *TokenManager) GenerateToken(namespace, recName, owner string, ttl time.Duration) (string, error) {
    claims := ApprovalClaims{
        RegisteredClaims: jwt.RegisteredClaims{
            Subject:   owner,
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
            Issuer:    "ovro",
        },
        Namespace: namespace,
        RecName:   recName,
        Owner:     owner,
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(tm.signingKey)
}
```

Each token encodes the recommendation's namespace, name, and owner. Tokens expire after 14 days (set by the API server when generating them). The signing key is either loaded from a Kubernetes Secret or auto-generated in demo mode.

### Validating Tokens

```go
func (tm *TokenManager) ValidateToken(tokenString string) (*ApprovalClaims, error) {
    token, err := jwt.ParseWithClaims(tokenString, &ApprovalClaims{},
        func(token *jwt.Token) (interface{}, error) {
            if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
                return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
            }
            return tm.signingKey, nil
        })
    // ...
}
```

Validation checks the signing method (must be HMAC), verifies the signature, and checks the expiration time.

## Owner Resolver

The Owner Resolver determines who owns a VM by checking Kubernetes labels:

```go
// internal/owner/resolver.go

func (r *Resolver) ResolveOwner(ctx context.Context, vmName, namespace string) (string, error) {
    // 1. Check VM labels
    vm := &unstructured.Unstructured{}
    vm.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
    r.Client.Get(ctx, types.NamespacedName{Name: vmName, Namespace: namespace}, vm)

    labels := vm.GetLabels()
    if owner, ok := labels[rightsizingv1alpha1.LabelOwner]; ok {
        return owner, nil
    }

    // 2. Fall back to namespace labels
    ns := &corev1.Namespace{}
    r.Client.Get(ctx, types.NamespacedName{Name: namespace}, ns)
    if owner, ok := ns.Labels[rightsizingv1alpha1.LabelOwner]; ok {
        return owner, nil
    }

    return "", nil // No owner = no approval needed
}
```

The precedence is: VM label > Namespace label > no owner. When no owner is found, the approval workflow is skipped and changes apply immediately.

## Approval Proxy

The Approval Proxy is a standalone HTTP server that sits between the external OpenShift Route and the internal backend API. It serves an HTML approval page where owners can review and act on recommendations.

```go
// internal/approvalproxy/proxy.go

type ApprovalProxy struct {
    tokenManager *approval.TokenManager
    backendURL   string
    httpClient   *http.Client
    mux          *http.ServeMux
    templates    *template.Template
}
```

### Flow

```
Owner clicks approval link
  -> GET /approve?token=<jwt>
  -> Proxy validates JWT
  -> Proxy fetches recommendation from backend API
  -> Proxy renders HTML approval page
  -> Owner clicks Approve/Reject
  -> POST /approve (form submission)
  -> Proxy validates JWT again
  -> Proxy forwards action to backend internal endpoint
  -> Backend updates recommendation status
  -> Proxy renders success/error page
```

### Template Rendering

The proxy uses Go's `html/template` with embedded HTML files:

```go
//go:embed static/*
var staticFS embed.FS

tmpl, err := template.ParseFS(staticFS, "static/*.html")
```

The approval page shows the full recommendation details: VM name, namespace, direction, current vs. recommended resources, utilisation metrics, and (for non-hotplug VMs) restart scheduling options.

### Security

The proxy is exposed via an OpenShift Route with TLS. Internal communication to the backend uses service-internal TLS. The JWT token is the only authentication — no OpenShift credentials are needed for the owner to approve.

Internal backend endpoints (`/api/v1/internal/...`) are registered without the auth middleware, since the proxy validates the JWT and passes the owner identity via the `X-Approval-Owner` header.

## Demo Mode

In demo mode, the API server auto-generates a signing key and skips the owner label check:

```go
// cmd/main.go

if demoMode && signingKeyPath == "" {
    demoKey := make([]byte, 32)
    rand.Read(demoKey)
    tokenMgr := approval.NewTokenManager(demoKey)
    // ...
}
```

This means the approval workflow can be demonstrated without any Kubernetes Secrets, owner labels, or notification configuration. The approval URL is displayed directly in the UI dialog.

## Key Takeaways

- JWT tokens encode the recommendation identity and owner, with 14-day expiry.
- Owner resolution follows VM label > namespace label > no owner precedence.
- The Approval Proxy is a standalone binary that validates tokens and serves HTML pages.
- Internal API endpoints bypass auth middleware — the proxy handles authentication via JWT.
- Demo mode auto-generates signing keys for zero-config approval workflow testing.

## Next Steps

When the approval workflow triggers, the owner needs to be notified. That's where the Notification System comes in.
