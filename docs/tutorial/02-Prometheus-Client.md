---
title: "Chapter 2: Prometheus Client"
order: 2
---

# Chapter 2: Prometheus Client

## Introduction

Before OVRO can recommend anything, it needs data. The Prometheus Client is the data collection layer — it queries the cluster's Prometheus/Thanos endpoint for KubeVirt VM CPU and memory utilisation metrics. Think of it as the operator's "eyes": without it, recommendations would be blind guesses.

## How It Works

OpenShift clusters with KubeVirt expose metrics like `kubevirt_vmi_cpu_usage_seconds_total` and `kubevirt_vmi_memory_resident_bytes` through their monitoring stack. OVRO queries these via the Thanos Querier (which aggregates Prometheus data across the cluster).

The client handles three concerns:
1. **Authentication** — reads the service account token for bearer auth to Thanos.
2. **PromQL queries** — builds time-series queries with appropriate lookback windows.
3. **Data transformation** — converts raw Prometheus responses into Go structs.

## Client Structure

```go
// internal/prometheus/client.go

type Client struct {
    baseURL     string
    httpClient  *http.Client
    bearerToken string
}

func NewClient(baseURL string) *Client {
    var token string
    if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
        token = strings.TrimSpace(string(data))
    }
    return &Client{
        baseURL:     baseURL,
        bearerToken: token,
        httpClient: &http.Client{
            Timeout: 30 * time.Second,
            Transport: &http.Transport{
                TLSClientConfig: &tls.Config{
                    InsecureSkipVerify: true,
                },
            },
        },
    }
}
```

The client reads its bearer token from the Kubernetes-mounted service account. The default Thanos endpoint is `https://thanos-querier.openshift-monitoring.svc:9091`.

## PromQL Queries

OVRO uses two queries per VM, defined in `internal/prometheus/queries.go`:

```go
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
```

- **CPU**: A sub-query that computes `rate()` (per-second CPU usage) over 5-minute windows, sampled every minute across the lookback period. This produces a time series of CPU utilisation values.
- **Memory**: A range vector of raw resident memory bytes sampled across the lookback period.

Note the `sanitizeLabelValue` function that escapes special characters to prevent PromQL injection:

```go
func sanitizeLabelValue(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `"`, `\"`)
    s = strings.ReplaceAll(s, "\n", "")
    s = strings.ReplaceAll(s, "\r", "")
    return s
}
```

## The Query Pipeline

The client provides three layers of abstraction:

1. **QueryRange** — low-level: executes any PromQL query and returns raw samples.
2. **GetVMMetrics** — mid-level: fetches both CPU and memory samples for a VM.
3. **GetVMUtilization** — high-level: computes P95 and max utilisation percentages, ready for the calculator.

```go
func (c *Client) GetVMUtilization(ctx context.Context, vmName, namespace string, lookbackDays int) (*VMUtilization, error) {
    metrics, err := c.GetVMMetrics(ctx, vmName, namespace, lookbackDays)
    if err != nil {
        return nil, fmt.Errorf("fetching VM metrics: %w", err)
    }

    utilization := &VMUtilization{
        CPUP95Percent:    calculator.ComputePercentile(metrics.CPUSamples, 95),
        MemoryP95Percent: calculator.ComputePercentile(metrics.MemorySamples, 95),
        CPUMaxPercent:    maxValue(metrics.CPUSamples),
        MemoryMaxPercent: maxValue(metrics.MemorySamples),
        DataPoints:       len(metrics.CPUSamples),
    }
    return utilization, nil
}
```

The `DataPoints` count is important — the controller uses it to ensure there's enough data before making a recommendation (at minimum 50% coverage of 7 days at 1-minute resolution = 5,040 data points).

## Relationship to the Calculator

Notice that `GetVMUtilization` uses `calculator.ComputePercentile` directly. The Prometheus client computes the statistical summaries, then the controller passes them to the calculator along with policy thresholds. This keeps the calculator as a pure function with no I/O dependencies.

## Key Takeaways

- The Prometheus client authenticates via the Kubernetes service account token.
- Two PromQL queries per VM: CPU rate and memory resident bytes.
- Input sanitisation prevents PromQL injection from VM names.
- Three abstraction layers (raw query, metrics, utilisation) keep concerns separated.
- The `DataPoints` count enables the controller to enforce minimum data requirements.

## Next Steps

Now that we have utilisation data, let's see how the Calculator turns those numbers into actionable recommendations.
