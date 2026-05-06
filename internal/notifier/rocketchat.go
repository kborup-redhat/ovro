package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// RocketChatForwarder sends notifications to Rocket.Chat
type RocketChatForwarder struct {
	serverURL string
	channel   string
	authToken string
	userID    string
}

// NewRocketChatForwarder creates a new Rocket.Chat forwarder
func NewRocketChatForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*RocketChatForwarder, error) {
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("rocketchat forwarder requires serverUrl")
	}
	if cfg.Channel == "" {
		return nil, fmt.Errorf("rocketchat forwarder requires channel")
	}
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("rocketchat forwarder requires secretRef")
	}

	namespace := "default"
	secretName := cfg.SecretRef

	secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	authToken, ok := secretData["authToken"]
	if !ok {
		return nil, fmt.Errorf("secret missing authToken key")
	}

	userID, ok := secretData["userId"]
	if !ok {
		return nil, fmt.Errorf("secret missing userId key")
	}

	return &RocketChatForwarder{
		serverURL: cfg.ServerURL,
		channel:   cfg.Channel,
		authToken: string(authToken),
		userID:    string(userID),
	}, nil
}

// Name returns the forwarder name
func (r *RocketChatForwarder) Name() string {
	return "rocketchat"
}

// Send sends a notification to Rocket.Chat
func (r *RocketChatForwarder) Send(ctx context.Context, n *Notification) error {
	message := fmt.Sprintf(
		"**VM Rightsizing Recommendation**\n\n"+
			"**VM:** %s\n"+
			"**Namespace:** %s\n"+
			"**Owner:** %s\n"+
			"**Direction:** %s\n"+
			"**Current:** %d CPU, %d MB Memory\n"+
			"**Recommended:** %d CPU, %d MB Memory\n\n"+
			"[Approve/Reject](%s)",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	payload := map[string]interface{}{
		"channel": r.channel,
		"text":    message,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/chat.postMessage", r.serverURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", r.authToken)
	req.Header.Set("X-User-Id", r.userID)

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
