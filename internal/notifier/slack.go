package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// SlackForwarder sends notifications to Slack via webhook
type SlackForwarder struct {
	webhookURL string
	channel    string
}

// NewSlackForwarder creates a new Slack forwarder
func NewSlackForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*SlackForwarder, error) {
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("slack forwarder requires secretRef")
	}

	// Extract namespace from secretRef (format: namespace/name)
	namespace := "default"
	secretName := cfg.SecretRef
	// Simple parsing - in production, this could be more sophisticated

	secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	webhookURL, ok := secretData["webhookUrl"]
	if !ok {
		return nil, fmt.Errorf("secret missing webhookUrl key")
	}

	return &SlackForwarder{
		webhookURL: string(webhookURL),
		channel:    cfg.Channel,
	}, nil
}

// Name returns the forwarder name
func (s *SlackForwarder) Name() string {
	return "slack"
}

// Send sends a notification to Slack
func (s *SlackForwarder) Send(ctx context.Context, n *Notification) error {
	message := fmt.Sprintf(
		"*VM Rightsizing Recommendation*\n\n"+
			"*VM:* %s\n"+
			"*Namespace:* %s\n"+
			"*Owner:* %s\n"+
			"*Direction:* %s\n"+
			"*Current:* %d CPU, %d MB Memory\n"+
			"*Recommended:* %d CPU, %d MB Memory\n\n"+
			"<%s|Approve/Reject>",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	payload := map[string]interface{}{
		"text": message,
	}

	if s.channel != "" {
		payload["channel"] = s.channel
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
