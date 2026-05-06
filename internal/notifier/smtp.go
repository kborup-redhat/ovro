package notifier

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPForwarder sends notifications via email
type SMTPForwarder struct {
	from       string
	to         string
	smtpServer string
	smtpPort   int
	username   string
	password   string
}

// NewSMTPForwarder creates a new SMTP forwarder
func NewSMTPForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*SMTPForwarder, error) {
	if cfg.From == "" || cfg.To == "" {
		return nil, fmt.Errorf("smtp forwarder requires from and to addresses")
	}
	if cfg.SMTPServer == "" {
		return nil, fmt.Errorf("smtp forwarder requires smtpServer")
	}
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587 // default SMTP port
	}

	var username, password string
	if cfg.SecretRef != "" {
		namespace := "default"
		secretName := cfg.SecretRef

		secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to get secret: %w", err)
		}

		if u, ok := secretData["username"]; ok {
			username = string(u)
		}
		if p, ok := secretData["password"]; ok {
			password = string(p)
		}
	}

	return &SMTPForwarder{
		from:       cfg.From,
		to:         cfg.To,
		smtpServer: cfg.SMTPServer,
		smtpPort:   cfg.SMTPPort,
		username:   username,
		password:   password,
	}, nil
}

// Name returns the forwarder name
func (s *SMTPForwarder) Name() string {
	return "smtp"
}

// Send sends a notification via email
func (s *SMTPForwarder) Send(ctx context.Context, n *Notification) error {
	// Template substitution for {{owner}}
	to := strings.ReplaceAll(s.to, "{{owner}}", n.Owner)

	subject := fmt.Sprintf("VM Rightsizing Recommendation: %s", n.VMName)
	body := fmt.Sprintf(
		"VM Rightsizing Recommendation\n\n"+
			"VM: %s\n"+
			"Namespace: %s\n"+
			"Owner: %s\n"+
			"Direction: %s\n"+
			"Current: %d CPU, %d MB Memory\n"+
			"Recommended: %d CPU, %d MB Memory\n\n"+
			"Approve or reject this recommendation at:\n%s",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	message := fmt.Sprintf("From: %s\r\n"+
		"To: %s\r\n"+
		"Subject: %s\r\n"+
		"\r\n"+
		"%s\r\n",
		s.from, to, subject, body)

	addr := fmt.Sprintf("%s:%d", s.smtpServer, s.smtpPort)

	var auth smtp.Auth
	if s.username != "" && s.password != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.smtpServer)
	}

	err := smtp.SendMail(addr, auth, s.from, []string{to}, []byte(message))
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}
