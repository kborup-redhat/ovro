package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type SlackForwarder struct {
	botToken        string
	fallbackChannel string
}

func NewSlackForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*SlackForwarder, error) {
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("slack forwarder requires secretRef")
	}

	namespace := "ovro-system"
	secretData, err := secretGetter.GetSecretData(ctx, cfg.SecretRef, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	botToken, ok := secretData["botToken"]
	if !ok {
		return nil, fmt.Errorf("secret missing botToken key")
	}

	return &SlackForwarder{
		botToken:        string(botToken),
		fallbackChannel: cfg.Channel,
	}, nil
}

func (s *SlackForwarder) Name() string {
	return "slack"
}

func (s *SlackForwarder) Send(ctx context.Context, n *Notification) error {
	channel, err := s.resolveChannel(ctx, n.Owner)
	if err != nil {
		return fmt.Errorf("resolving slack channel for owner %q: %w", n.Owner, err)
	}

	message := fmt.Sprintf(
		"*VM Rightsizing Recommendation*\n\n"+
			"*VM:* %s\n"+
			"*Namespace:* %s\n"+
			"*Owner:* %s\n"+
			"*Direction:* %s\n"+
			"*Current:* %d CPU, %d GiB Memory\n"+
			"*Recommended:* %d CPU, %d GiB Memory\n\n"+
			"<%s|Approve/Reject>",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	payload := map[string]interface{}{
		"channel": channel,
		"text":    message,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshalling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://slack.com/api/chat.postMessage", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parsing slack response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("slack API error: %s", result.Error)
	}

	return nil
}

// resolveChannel determines where to send the message.
// If the owner looks like a channel (starts with # or C), use it directly.
// If it looks like an email, look up the Slack user and open a DM.
// Falls back to the configured fallbackChannel.
func (s *SlackForwarder) resolveChannel(ctx context.Context, owner string) (string, error) {
	if strings.HasPrefix(owner, "#") || strings.HasPrefix(owner, "C") {
		return owner, nil
	}

	if strings.Contains(owner, "@") {
		// Try the username part (before @) first — matches Slack handles in many orgs
		username := owner[:strings.Index(owner, "@")]
		userID, err := s.lookupUserByEmail(ctx, username)
		if err == nil && userID != "" {
			return userID, nil
		}
		// Fall back to full email lookup
		userID, err = s.lookupUserByEmail(ctx, owner)
		if err == nil && userID != "" {
			return userID, nil
		}
	}

	if s.fallbackChannel != "" {
		return s.fallbackChannel, nil
	}

	return "", fmt.Errorf("no channel resolved: owner %q is not a channel or known email, and no fallback channel configured", owner)
}

func (s *SlackForwarder) lookupUserByEmail(ctx context.Context, email string) (string, error) {
	reqURL := "https://slack.com/api/users.lookupByEmail?email=" + url.QueryEscape(email)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.botToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		OK   bool `json:"ok"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if !result.OK {
		return "", fmt.Errorf("users.lookupByEmail: %s", result.Error)
	}
	return result.User.ID, nil
}
