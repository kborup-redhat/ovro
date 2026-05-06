---
title: "Chapter 8: Notification System"
order: 8
---

# Chapter 8: Notification System

## Introduction

When a rightsizing recommendation requires owner approval, someone needs to tell the owner. OVRO's notification system is a pluggable, multi-channel dispatcher that can send approval requests via Slack DMs, Microsoft Teams messages, email, PagerDuty incidents, Rocket.Chat posts, ServiceNow tickets, and SNMP traps. Think of it like a mail room that receives one message and delivers it through every channel configured by the administrator.

## Architecture

The notification system follows a clean interface-based design:

```go
// internal/notifier/notifier.go

type Forwarder interface {
    Name() string
    Send(ctx context.Context, n *Notification) error
}

type Dispatcher struct {
    forwarders []Forwarder
    log        logr.Logger
}
```

Each notification channel implements the `Forwarder` interface. The `Dispatcher` iterates over all registered forwarders and sends to each one. Failures in one channel don't block the others.

## Notification Payload

Every forwarder receives the same `Notification` struct:

```go
type Notification struct {
    VMName        string
    Namespace     string
    Owner         string
    Direction     string
    CurrentCPU    int32
    CurrentMemory int32
    RecCPU        int32
    RecMemory     int32
    ApprovalURL   string
}
```

Each forwarder formats this data appropriately for its channel (Slack uses mrkdwn, Teams uses HTML, email uses plain text, etc.).

## Dispatcher

The dispatcher supports two send modes:

```go
func (d *Dispatcher) SendAll(ctx context.Context, n *Notification) []error {
    return d.sendFiltered(ctx, n, nil)
}

func (d *Dispatcher) SendAllExcept(ctx context.Context, n *Notification, excludeTypes []string) []error {
    exclude := make(map[string]bool, len(excludeTypes))
    for _, t := range excludeTypes {
        exclude[t] = true
    }
    return d.sendFiltered(ctx, n, exclude)
}
```

`SendAll` is used for initial notifications. `SendAllExcept` is used for 7-day reminders, where ServiceNow is excluded because the ticket already exists.

## Forwarder Implementations

### Slack

Uses the Slack Bot API (`chat.postMessage` and `users.lookupByEmail`):

```go
type SlackForwarder struct {
    botToken        string
    fallbackChannel string
}
```

Owner routing: if the owner label is an email (`user@company.com`), Slack tries to look up the username part first (`user`), then the full email, then falls back to the configured channel. If the owner starts with `#` or `C`, it posts directly to that channel.

### Microsoft Teams

Uses the Microsoft Graph API with OAuth2 client credentials:

```go
type TeamsForwarder struct {
    tenantID        string
    clientID        string
    clientSecret    string
    fallbackChannel string // webhook URL
}
```

For email owners, Teams looks up the user via Graph API and creates a 1:1 chat. If user lookup fails, it falls back to an incoming webhook URL for channel posting, sending an Adaptive Card.

### SMTP

Supports three TLS modes:

```go
type SMTPForwarder struct {
    from          string
    to            string
    smtpServer    string
    smtpPort      int
    tlsMode       string // "starttls", "tls", or "none"
    tlsSkipVerify bool
    username      string
    password      string
}
```

- **starttls** (port 587) — connects plain, upgrades with STARTTLS.
- **tls** (port 465) — connects with TLS from the start.
- **none** — no encryption (not recommended for production).

The `to` field supports `{{owner}}` substitution — SMTP is the only forwarder that sends directly to the owner's email address.

### PagerDuty

Creates incidents via the PagerDuty Events API v2 with a routing key.

### Rocket.Chat

Posts to a channel via the Rocket.Chat REST API using personal auth tokens.

### ServiceNow

Creates incidents via the ServiceNow Table API with assignment group and category configuration.

### SNMP

Currently a stub that logs trap messages — full SNMP trap sending can be added with a library like `gosnmp`.

## Configuration

Forwarders are configured via a ConfigMap (`ovro-notifications`) in the `ovro-system` namespace:

```yaml
forwarders:
  - type: slack
    enabled: true
    channel: "#vm-rightsizing"    # fallback channel
    secretRef: ovro-slack-credentials
  - type: smtp
    enabled: true
    from: "ovro@company.com"
    to: "{{owner}}"
    smtpServer: "smtp.company.com"
    smtpPort: 587
    smtpTLS: "starttls"
    secretRef: ovro-smtp-credentials
```

Each forwarder that needs credentials references a Kubernetes Secret. The dispatcher factory skips disabled forwarders and logs unknown types.

## Creating the Dispatcher

```go
func NewDispatcher(cfg NotifierConfig, secretGetter SecretGetter, log logr.Logger) (*Dispatcher, error) {
    for _, fwdCfg := range cfg.Forwarders {
        if !fwdCfg.Enabled { continue }
        switch fwdCfg.Type {
        case "slack":   fwd, err = NewSlackForwarder(ctx, fwdCfg, secretGetter)
        case "teams":   fwd, err = NewTeamsForwarder(ctx, fwdCfg, secretGetter)
        case "smtp":    fwd, err = NewSMTPForwarder(ctx, fwdCfg, secretGetter)
        // ... etc
        }
        forwarders = append(forwarders, fwd)
    }
    return &Dispatcher{forwarders: forwarders, log: log}, nil
}
```

The `SecretGetter` interface abstracts Kubernetes secret access, making the dispatcher testable without a real cluster.

## Key Takeaways

- The `Forwarder` interface enables adding new notification channels without changing the dispatcher.
- `SendAllExcept` supports selective delivery (e.g., skipping ServiceNow for reminders).
- Slack and Teams resolve the notification target dynamically from the owner label.
- SMTP is the only forwarder that uses the owner label value directly as a recipient address.
- Configuration lives in a ConfigMap; credentials live in Secrets.

## Next Steps

Notifications reach the owner; the approval proxy handles their response. But the console plugin needs an API to talk to. Let's look at the REST API Server.
