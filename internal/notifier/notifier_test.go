package notifier

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v3"
)

// mockForwarder is a simple mock implementation of the Forwarder interface
type mockForwarder struct {
	name      string
	sendError error
	sendCalls int
}

func (m *mockForwarder) Name() string {
	return m.name
}

func (m *mockForwarder) Send(ctx context.Context, n *Notification) error {
	m.sendCalls++
	return m.sendError
}

// mockSecretGetter is a mock implementation of SecretGetter
type mockSecretGetter struct {
	secrets map[string]map[string][]byte
}

func (m *mockSecretGetter) GetSecretData(ctx context.Context, name, namespace string) (map[string][]byte, error) {
	if m.secrets == nil {
		return nil, errors.New("no secrets configured")
	}
	if data, ok := m.secrets[name]; ok {
		return data, nil
	}
	return nil, errors.New("secret not found")
}

func TestDispatcher_SendAll_NoForwarders(t *testing.T) {
	d := &Dispatcher{
		forwarders: []Forwarder{},
		log:        logr.Discard(),
	}

	n := &Notification{
		VMName:    "test-vm",
		Namespace: "default",
	}

	errs := d.SendAll(context.Background(), n)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %d", len(errs))
	}
}

func TestDispatcher_SendAll_AllForwarders(t *testing.T) {
	mock1 := &mockForwarder{name: "mock1"}
	mock2 := &mockForwarder{name: "mock2"}
	mock3 := &mockForwarder{name: "mock3"}

	d := &Dispatcher{
		forwarders: []Forwarder{mock1, mock2, mock3},
		log:        logr.Discard(),
	}

	n := &Notification{
		VMName:    "test-vm",
		Namespace: "default",
	}

	errs := d.SendAll(context.Background(), n)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %d", len(errs))
	}

	if mock1.sendCalls != 1 {
		t.Errorf("expected mock1 to be called once, got %d", mock1.sendCalls)
	}
	if mock2.sendCalls != 1 {
		t.Errorf("expected mock2 to be called once, got %d", mock2.sendCalls)
	}
	if mock3.sendCalls != 1 {
		t.Errorf("expected mock3 to be called once, got %d", mock3.sendCalls)
	}
}

func TestDispatcher_SendAll_OneFailure(t *testing.T) {
	mock1 := &mockForwarder{name: "mock1"}
	mock2 := &mockForwarder{name: "mock2", sendError: errors.New("send failed")}
	mock3 := &mockForwarder{name: "mock3"}

	d := &Dispatcher{
		forwarders: []Forwarder{mock1, mock2, mock3},
		log:        logr.Discard(),
	}

	n := &Notification{
		VMName:    "test-vm",
		Namespace: "default",
	}

	errs := d.SendAll(context.Background(), n)
	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d", len(errs))
	}

	// Verify all forwarders were still called
	if mock1.sendCalls != 1 {
		t.Errorf("expected mock1 to be called once, got %d", mock1.sendCalls)
	}
	if mock2.sendCalls != 1 {
		t.Errorf("expected mock2 to be called once, got %d", mock2.sendCalls)
	}
	if mock3.sendCalls != 1 {
		t.Errorf("expected mock3 to be called once, got %d", mock3.sendCalls)
	}
}

func TestNewDispatcher_DisabledForwardersSkipped(t *testing.T) {
	cfg := NotifierConfig{
		Forwarders: []ForwarderConfig{
			{
				Type:      "slack",
				Enabled:   false,
				SecretRef: "slack-secret",
			},
			{
				Type:      "teams",
				Enabled:   false,
				SecretRef: "teams-secret",
			},
		},
	}

	secretGetter := &mockSecretGetter{
		secrets: map[string]map[string][]byte{
			"slack-secret": {"webhookUrl": []byte("https://hooks.slack.com/test")},
			"teams-secret": {"webhookUrl": []byte("https://outlook.office.com/test")},
		},
	}

	d, err := NewDispatcher(cfg, secretGetter, logr.Discard())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(d.forwarders) != 0 {
		t.Errorf("expected 0 forwarders, got %d", len(d.forwarders))
	}
}

func TestNewDispatcher_EnabledForwardersCreated(t *testing.T) {
	cfg := NotifierConfig{
		Forwarders: []ForwarderConfig{
			{
				Type:      "slack",
				Enabled:   true,
				SecretRef: "slack-secret",
			},
			{
				Type:      "teams",
				Enabled:   true,
				SecretRef: "teams-secret",
			},
		},
	}

	secretGetter := &mockSecretGetter{
		secrets: map[string]map[string][]byte{
			"slack-secret": {"webhookUrl": []byte("https://hooks.slack.com/test")},
			"teams-secret": {"webhookUrl": []byte("https://outlook.office.com/test")},
		},
	}

	d, err := NewDispatcher(cfg, secretGetter, logr.Discard())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(d.forwarders) != 2 {
		t.Errorf("expected 2 forwarders, got %d", len(d.forwarders))
	}
}

func TestConfigParsing_AllForwarderTypes(t *testing.T) {
	yamlData := `
forwarders:
  - type: slack
    enabled: true
    channel: "#alerts"
    secretRef: slack-secret
  - type: teams
    enabled: true
    secretRef: teams-secret
  - type: smtp
    enabled: true
    from: ovro@example.com
    to: "{{owner}}"
    smtpServer: smtp.example.com
    smtpPort: 587
    secretRef: smtp-secret
  - type: snmp
    enabled: true
    host: snmp.example.com
    port: 162
    community: public
  - type: pagerduty
    enabled: true
    secretRef: pd-secret
  - type: rocketchat
    enabled: true
    serverUrl: https://rocket.example.com
    channel: "#alerts"
    secretRef: rc-secret
  - type: servicenow
    enabled: true
    instanceUrl: https://instance.service-now.com
    assignmentGroup: "VM Ops"
    category: "Infrastructure"
    secretRef: snow-secret
`

	var cfg NotifierConfig
	err := yaml.Unmarshal([]byte(yamlData), &cfg)
	if err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	if len(cfg.Forwarders) != 7 {
		t.Errorf("expected 7 forwarders, got %d", len(cfg.Forwarders))
	}

	// Verify types
	expectedTypes := []string{"slack", "teams", "smtp", "snmp", "pagerduty", "rocketchat", "servicenow"}
	for i, fwd := range cfg.Forwarders {
		if fwd.Type != expectedTypes[i] {
			t.Errorf("expected type %s at index %d, got %s", expectedTypes[i], i, fwd.Type)
		}
		if !fwd.Enabled {
			t.Errorf("expected forwarder %d to be enabled", i)
		}
	}

	// Verify specific config fields
	if cfg.Forwarders[0].Channel != "#alerts" {
		t.Errorf("expected slack channel #alerts, got %s", cfg.Forwarders[0].Channel)
	}
	if cfg.Forwarders[2].SMTPPort != 587 {
		t.Errorf("expected smtp port 587, got %d", cfg.Forwarders[2].SMTPPort)
	}
	if cfg.Forwarders[2].To != "{{owner}}" {
		t.Errorf("expected smtp to {{owner}}, got %s", cfg.Forwarders[2].To)
	}
	if cfg.Forwarders[3].Port != 162 {
		t.Errorf("expected snmp port 162, got %d", cfg.Forwarders[3].Port)
	}
	if cfg.Forwarders[6].AssignmentGroup != "VM Ops" {
		t.Errorf("expected servicenow assignment group 'VM Ops', got %s", cfg.Forwarders[6].AssignmentGroup)
	}
}

func TestNewDispatcher_UnknownForwarderType(t *testing.T) {
	cfg := NotifierConfig{
		Forwarders: []ForwarderConfig{
			{
				Type:    "unknown",
				Enabled: true,
			},
		},
	}

	secretGetter := &mockSecretGetter{}

	d, err := NewDispatcher(cfg, secretGetter, logr.Discard())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Unknown types are logged and skipped, not errored
	if len(d.forwarders) != 0 {
		t.Errorf("expected 0 forwarders, got %d", len(d.forwarders))
	}
}

func TestSNMPForwarder_Creation(t *testing.T) {
	cfg := ForwarderConfig{
		Type: "snmp",
		Host: "snmp.example.com",
		Port: 162,
		Community: "public",
	}

	fwd, err := NewSNMPForwarder(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fwd.Name() != "snmp" {
		t.Errorf("expected name 'snmp', got %s", fwd.Name())
	}

	// SNMP Send should not error (it just logs for MVP)
	n := &Notification{
		VMName:    "test-vm",
		Namespace: "default",
	}
	err = fwd.Send(context.Background(), n)
	if err != nil {
		t.Errorf("unexpected error from SNMP send: %v", err)
	}
}
