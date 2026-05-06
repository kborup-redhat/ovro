package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// PagerDutyForwarder sends notifications to PagerDuty
type PagerDutyForwarder struct {
	routingKey string
}

// NewPagerDutyForwarder creates a new PagerDuty forwarder
func NewPagerDutyForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*PagerDutyForwarder, error) {
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("pagerduty forwarder requires secretRef")
	}

	namespace := "default"
	secretName := cfg.SecretRef

	secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	routingKey, ok := secretData["routingKey"]
	if !ok {
		return nil, fmt.Errorf("secret missing routingKey key")
	}

	return &PagerDutyForwarder{
		routingKey: string(routingKey),
	}, nil
}

// Name returns the forwarder name
func (p *PagerDutyForwarder) Name() string {
	return "pagerduty"
}

// Send sends a notification to PagerDuty Events API v2
func (p *PagerDutyForwarder) Send(ctx context.Context, n *Notification) error {
	payload := map[string]interface{}{
		"routing_key":  p.routingKey,
		"event_action": "trigger",
		"payload": map[string]interface{}{
			"summary":  fmt.Sprintf("VM Rightsizing: %s/%s (%s)", n.Namespace, n.VMName, n.Direction),
			"severity": "info",
			"source":   "ovro",
			"custom_details": map[string]interface{}{
				"vm":               n.VMName,
				"namespace":        n.Namespace,
				"owner":            n.Owner,
				"direction":        n.Direction,
				"current_cpu":      n.CurrentCPU,
				"current_memory":   n.CurrentMemory,
				"recommended_cpu":  n.RecCPU,
				"recommended_memory": n.RecMemory,
				"approval_url":     n.ApprovalURL,
			},
		},
		"links": []map[string]string{
			{
				"href": n.ApprovalURL,
				"text": "Approve/Reject",
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://events.pagerduty.com/v2/enqueue", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}
