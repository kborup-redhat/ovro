package notifier

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
)

// Notification represents a VM rightsizing notification
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

// Forwarder sends notifications to external systems
type Forwarder interface {
	Name() string
	Send(ctx context.Context, n *Notification) error
}

// ForwarderConfig represents configuration for a single forwarder
type ForwarderConfig struct {
	Type            string `yaml:"type"`
	Enabled         bool   `yaml:"enabled"`
	Channel         string `yaml:"channel,omitempty"`
	SecretRef       string `yaml:"secretRef,omitempty"`
	From            string `yaml:"from,omitempty"`
	To              string `yaml:"to,omitempty"`
	SMTPServer      string `yaml:"smtpServer,omitempty"`
	SMTPPort        int    `yaml:"smtpPort,omitempty"`
	Host            string `yaml:"host,omitempty"`
	Port            int    `yaml:"port,omitempty"`
	Community       string `yaml:"community,omitempty"`
	ServerURL       string `yaml:"serverUrl,omitempty"`
	InstanceURL     string `yaml:"instanceUrl,omitempty"`
	AssignmentGroup string `yaml:"assignmentGroup,omitempty"`
	Category        string `yaml:"category,omitempty"`
}

// NotifierConfig represents the complete notifier configuration
type NotifierConfig struct {
	Forwarders []ForwarderConfig `yaml:"forwarders"`
}

// SecretGetter retrieves secret data from the cluster
type SecretGetter interface {
	GetSecretData(ctx context.Context, name, namespace string) (map[string][]byte, error)
}

// Dispatcher manages and sends notifications to multiple forwarders
type Dispatcher struct {
	forwarders []Forwarder
	log        logr.Logger
}

// NewDispatcher creates a new notification dispatcher from configuration
func NewDispatcher(cfg NotifierConfig, secretGetter SecretGetter, log logr.Logger) (*Dispatcher, error) {
	var forwarders []Forwarder
	ctx := context.Background()

	for _, fwdCfg := range cfg.Forwarders {
		if !fwdCfg.Enabled {
			log.V(1).Info("skipping disabled forwarder", "type", fwdCfg.Type)
			continue
		}

		var fwd Forwarder
		var err error

		switch fwdCfg.Type {
		case "slack":
			fwd, err = NewSlackForwarder(ctx, fwdCfg, secretGetter)
		case "teams":
			fwd, err = NewTeamsForwarder(ctx, fwdCfg, secretGetter)
		case "smtp":
			fwd, err = NewSMTPForwarder(ctx, fwdCfg, secretGetter)
		case "snmp":
			fwd, err = NewSNMPForwarder(fwdCfg)
		case "pagerduty":
			fwd, err = NewPagerDutyForwarder(ctx, fwdCfg, secretGetter)
		case "rocketchat":
			fwd, err = NewRocketChatForwarder(ctx, fwdCfg, secretGetter)
		case "servicenow":
			fwd, err = NewServiceNowForwarder(ctx, fwdCfg, secretGetter)
		default:
			log.Error(nil, "unknown forwarder type", "type", fwdCfg.Type)
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create %s forwarder: %w", fwdCfg.Type, err)
		}

		forwarders = append(forwarders, fwd)
		log.Info("registered forwarder", "type", fwdCfg.Type, "name", fwd.Name())
	}

	return &Dispatcher{
		forwarders: forwarders,
		log:        log,
	}, nil
}

// SendAll sends the notification to all registered forwarders
func (d *Dispatcher) SendAll(ctx context.Context, n *Notification) []error {
	return d.sendFiltered(ctx, n, nil)
}

// SendAllExcept sends the notification to all registered forwarders except those
// whose Name() matches one of the excluded types.
func (d *Dispatcher) SendAllExcept(ctx context.Context, n *Notification, excludeTypes []string) []error {
	exclude := make(map[string]bool, len(excludeTypes))
	for _, t := range excludeTypes {
		exclude[t] = true
	}
	return d.sendFiltered(ctx, n, exclude)
}

func (d *Dispatcher) sendFiltered(ctx context.Context, n *Notification, exclude map[string]bool) []error {
	var errors []error

	for _, fwd := range d.forwarders {
		if exclude != nil && exclude[fwd.Name()] {
			d.log.V(1).Info("skipping forwarder", "forwarder", fwd.Name(), "vm", n.VMName)
			continue
		}
		d.log.V(1).Info("sending notification", "forwarder", fwd.Name(), "vm", n.VMName)
		if err := fwd.Send(ctx, n); err != nil {
			d.log.Error(err, "failed to send notification", "forwarder", fwd.Name(), "vm", n.VMName)
			errors = append(errors, fmt.Errorf("%s: %w", fwd.Name(), err))
		} else {
			d.log.Info("notification sent successfully", "forwarder", fwd.Name(), "vm", n.VMName)
		}
	}

	return errors
}
