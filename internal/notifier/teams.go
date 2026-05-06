package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// TeamsForwarder sends notifications to Microsoft Teams via webhook
type TeamsForwarder struct {
	webhookURL string
}

// NewTeamsForwarder creates a new Teams forwarder
func NewTeamsForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*TeamsForwarder, error) {
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("teams forwarder requires secretRef")
	}

	namespace := "default"
	secretName := cfg.SecretRef

	secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	webhookURL, ok := secretData["webhookUrl"]
	if !ok {
		return nil, fmt.Errorf("secret missing webhookUrl key")
	}

	return &TeamsForwarder{
		webhookURL: string(webhookURL),
	}, nil
}

// Name returns the forwarder name
func (t *TeamsForwarder) Name() string {
	return "teams"
}

// Send sends a notification to Teams using Adaptive Card format
func (t *TeamsForwarder) Send(ctx context.Context, n *Notification) error {
	// Microsoft Teams Adaptive Card format
	card := map[string]interface{}{
		"type": "message",
		"attachments": []map[string]interface{}{
			{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]interface{}{
					"type": "AdaptiveCard",
					"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
					"version": "1.2",
					"body": []map[string]interface{}{
						{
							"type": "TextBlock",
							"text": "VM Rightsizing Recommendation",
							"weight": "Bolder",
							"size": "Medium",
						},
						{
							"type": "FactSet",
							"facts": []map[string]string{
								{"title": "VM", "value": n.VMName},
								{"title": "Namespace", "value": n.Namespace},
								{"title": "Owner", "value": n.Owner},
								{"title": "Direction", "value": n.Direction},
								{"title": "Current CPU", "value": fmt.Sprintf("%d", n.CurrentCPU)},
								{"title": "Current Memory", "value": fmt.Sprintf("%d MB", n.CurrentMemory)},
								{"title": "Recommended CPU", "value": fmt.Sprintf("%d", n.RecCPU)},
								{"title": "Recommended Memory", "value": fmt.Sprintf("%d MB", n.RecMemory)},
							},
						},
					},
					"actions": []map[string]interface{}{
						{
							"type": "Action.OpenUrl",
							"title": "Approve/Reject",
							"url": n.ApprovalURL,
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", t.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
