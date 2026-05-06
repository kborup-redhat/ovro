package prometheus

import (
	"fmt"
	"strings"
)

func sanitizeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func cpuUsageQuery(vmName, namespace, lookback string) string {
	return fmt.Sprintf(
		`rate(kubevirt_vmi_cpu_usage_seconds_total{name="%s",namespace="%s"}[5m])[%s:1m]`,
		sanitizeLabelValue(vmName), sanitizeLabelValue(namespace), lookback,
	)
}

func memoryResidentQuery(vmName, namespace, lookback string) string {
	return fmt.Sprintf(
		`kubevirt_vmi_memory_resident_bytes{name="%s",namespace="%s"}[%s]`,
		sanitizeLabelValue(vmName), sanitizeLabelValue(namespace), lookback,
	)
}
