package notifier

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

type SMTPForwarder struct {
	from            string
	to              string
	smtpServer      string
	smtpPort        int
	tlsMode         string // "starttls", "tls", or "none"
	tlsSkipVerify   bool
	username        string
	password        string
}

func NewSMTPForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*SMTPForwarder, error) {
	if cfg.From == "" || cfg.To == "" {
		return nil, fmt.Errorf("smtp forwarder requires from and to addresses")
	}
	if cfg.SMTPServer == "" {
		return nil, fmt.Errorf("smtp forwarder requires smtpServer")
	}
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587
	}

	tlsMode := strings.ToLower(cfg.SMTPTLS)
	if tlsMode == "" {
		if cfg.SMTPPort == 465 {
			tlsMode = "tls"
		} else {
			tlsMode = "starttls"
		}
	}

	var username, password string
	if cfg.SecretRef != "" {
		namespace := "ovro-system"
		secretData, err := secretGetter.GetSecretData(ctx, cfg.SecretRef, namespace)
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
		from:          cfg.From,
		to:            cfg.To,
		smtpServer:    cfg.SMTPServer,
		smtpPort:      cfg.SMTPPort,
		tlsMode:       tlsMode,
		tlsSkipVerify: cfg.SMTPTLSSkipVerify,
		username:      username,
		password:      password,
	}, nil
}

func (s *SMTPForwarder) Name() string {
	return "smtp"
}

func (s *SMTPForwarder) Send(ctx context.Context, n *Notification) error {
	to := strings.ReplaceAll(s.to, "{{owner}}", n.Owner)

	subject := fmt.Sprintf("VM Rightsizing Recommendation: %s", n.VMName)
	body := fmt.Sprintf(
		"VM Rightsizing Recommendation\n\n"+
			"VM: %s\n"+
			"Namespace: %s\n"+
			"Owner: %s\n"+
			"Direction: %s\n"+
			"Current: %d CPU, %d GiB Memory\n"+
			"Recommended: %d CPU, %d GiB Memory\n\n"+
			"Approve or reject this recommendation at:\n%s",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	message := fmt.Sprintf("From: %s\r\n"+
		"To: %s\r\n"+
		"Subject: %s\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n"+
		"\r\n"+
		"%s\r\n",
		s.from, to, subject, body)

	addr := fmt.Sprintf("%s:%d", s.smtpServer, s.smtpPort)
	tlsCfg := &tls.Config{
		ServerName:         s.smtpServer,
		InsecureSkipVerify: s.tlsSkipVerify,
	}

	var auth smtp.Auth
	if s.username != "" && s.password != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.smtpServer)
	}

	switch s.tlsMode {
	case "tls":
		return s.sendImplicitTLS(addr, tlsCfg, auth, to, []byte(message))
	case "none":
		return s.sendPlain(addr, auth, to, []byte(message))
	default:
		return s.sendSTARTTLS(addr, tlsCfg, auth, to, []byte(message))
	}
}

// sendSTARTTLS connects in plain text, upgrades to TLS via STARTTLS (port 587).
func (s *SMTPForwarder) sendSTARTTLS(addr string, tlsCfg *tls.Config, auth smtp.Auth, to string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", addr, err)
	}
	defer c.Close()

	if err := c.StartTLS(tlsCfg); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	return s.deliver(c, auth, to, msg)
}

// sendImplicitTLS connects with TLS from the start (port 465).
func (s *SMTPForwarder) sendImplicitTLS(addr string, tlsCfg *tls.Config, auth smtp.Auth, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("TLS dial %s: %w", addr, err)
	}

	host, _, _ := net.SplitHostPort(addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("creating SMTP client: %w", err)
	}
	defer c.Close()

	return s.deliver(c, auth, to, msg)
}

// sendPlain connects without any encryption.
func (s *SMTPForwarder) sendPlain(addr string, auth smtp.Auth, to string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", addr, err)
	}
	defer c.Close()

	return s.deliver(c, auth, to, msg)
}

func (s *SMTPForwarder) deliver(c *smtp.Client, auth smtp.Auth, to string, msg []byte) error {
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(s.from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing DATA: %w", err)
	}
	return c.Quit()
}
