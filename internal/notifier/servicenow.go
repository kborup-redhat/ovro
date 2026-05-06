package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ServiceNowForwarder creates incidents in ServiceNow
type ServiceNowForwarder struct {
	instanceURL     string
	username        string
	password        string
	assignmentGroup string
	category        string
}

// NewServiceNowForwarder creates a new ServiceNow forwarder
func NewServiceNowForwarder(ctx context.Context, cfg ForwarderConfig, secretGetter SecretGetter) (*ServiceNowForwarder, error) {
	if cfg.InstanceURL == "" {
		return nil, fmt.Errorf("servicenow forwarder requires instanceUrl")
	}
	if cfg.SecretRef == "" {
		return nil, fmt.Errorf("servicenow forwarder requires secretRef")
	}

	namespace := "default"
	secretName := cfg.SecretRef

	secretData, err := secretGetter.GetSecretData(ctx, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}

	username, ok := secretData["username"]
	if !ok {
		return nil, fmt.Errorf("secret missing username key")
	}

	password, ok := secretData["password"]
	if !ok {
		return nil, fmt.Errorf("secret missing password key")
	}

	return &ServiceNowForwarder{
		instanceURL:     cfg.InstanceURL,
		username:        string(username),
		password:        string(password),
		assignmentGroup: cfg.AssignmentGroup,
		category:        cfg.Category,
	}, nil
}

// Name returns the forwarder name
func (s *ServiceNowForwarder) Name() string {
	return "servicenow"
}

// Send creates an incident in ServiceNow
func (s *ServiceNowForwarder) Send(ctx context.Context, n *Notification) error {
	shortDescription := fmt.Sprintf("VM Rightsizing: %s/%s (%s)", n.Namespace, n.VMName, n.Direction)
	description := fmt.Sprintf(
		"VM Rightsizing Recommendation\n\n"+
			"VM: %s\n"+
			"Namespace: %s\n"+
			"Owner: %s\n"+
			"Direction: %s\n"+
			"Current: %d CPU, %d MB Memory\n"+
			"Recommended: %d CPU, %d MB Memory\n\n"+
			"Approval URL: %s",
		n.VMName, n.Namespace, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
		n.ApprovalURL,
	)

	payload := map[string]interface{}{
		"short_description": shortDescription,
		"description":       description,
		"urgency":           "3",
		"impact":            "3",
	}

	if s.assignmentGroup != "" {
		payload["assignment_group"] = s.assignmentGroup
	}
	if s.category != "" {
		payload["category"] = s.category
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/now/table/incident", s.instanceURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(s.username, s.password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Parse response to get incident number (caller can store this if needed)
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	// The incident number is available in result["result"]["number"]
	// Caller can extract and store this if needed

	return nil
}
