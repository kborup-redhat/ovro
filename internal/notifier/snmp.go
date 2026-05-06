package notifier

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
)

// SNMPForwarder sends notifications via SNMP traps
type SNMPForwarder struct {
	host      string
	port      int
	community string
	log       logr.Logger
}

// NewSNMPForwarder creates a new SNMP forwarder
func NewSNMPForwarder(cfg ForwarderConfig) (*SNMPForwarder, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("snmp forwarder requires host")
	}
	if cfg.Port == 0 {
		cfg.Port = 162 // default SNMP trap port
	}

	return &SNMPForwarder{
		host:      cfg.Host,
		port:      cfg.Port,
		community: cfg.Community,
		log:       logr.Discard(), // Will be set by caller if needed
	}, nil
}

// Name returns the forwarder name
func (s *SNMPForwarder) Name() string {
	return "snmp"
}

// Send sends a notification via SNMP trap
// For MVP, this logs the notification. Full SNMP trap implementation can be added later.
func (s *SNMPForwarder) Send(ctx context.Context, n *Notification) error {
	// Format the trap message
	message := fmt.Sprintf(
		"VM Rightsizing: %s/%s (owner: %s) - Direction: %s, Current: %dCPU/%dMB, Recommended: %dCPU/%dMB",
		n.Namespace, n.VMName, n.Owner, n.Direction,
		n.CurrentCPU, n.CurrentMemory,
		n.RecCPU, n.RecMemory,
	)

	// For MVP, log the notification
	// In production, this would send an actual SNMP trap to s.host:s.port
	// using a library like github.com/gosnmp/gosnmp
	s.log.Info("SNMP trap (simulated)",
		"host", s.host,
		"port", s.port,
		"community", s.community,
		"message", message,
	)

	// TODO: Implement actual SNMP trap sending
	// Example using gosnmp:
	// params := &gosnmp.GoSNMP{
	//     Target:    s.host,
	//     Port:      uint16(s.port),
	//     Community: s.community,
	//     Version:   gosnmp.Version2c,
	//     Timeout:   time.Duration(2) * time.Second,
	// }
	// err := params.Connect()
	// if err != nil {
	//     return fmt.Errorf("failed to connect: %w", err)
	// }
	// defer params.Conn.Close()
	//
	// trap := gosnmp.SnmpTrap{
	//     Variables: []gosnmp.SnmpPDU{
	//         {Name: "1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(time.Now().Unix())},
	//         {Name: "1.3.6.1.6.3.1.1.4.1.0", Type: gosnmp.OctetString, Value: message},
	//     },
	// }
	// _, err = params.SendTrap(trap)
	// return err

	return nil
}
