---
title: "Chapter 2: Prometheus Integration"
order: 2
---

# Chapter 2: Prometheus Integration

In [Chapter 1](01-Overview.md), we introduced OVRO and its mission: right-sizing OpenShift Virtualization workloads based on real utilization data. But where does that data come from? Before we can recommend whether a VM should be downsized or upsized, we need hard numbers -- CPU usage rates, memory consumption over time, sampled at regular intervals across days or weeks.

That is the job of the Prometheus integration layer. Think of it as a **medical lab that runs blood tests**: it sends precisely formulated queries to Prometheus, collects the raw time-series samples that come back, processes them into structured results, and delivers a concise summary report (P95 utilization, maximum peaks) that downstream components can act on. The "patient" is a KubeVirt virtual machine; the "blood work" is CPU and memory metrics.

This chapter walks through the two files that implement this layer:

| File | Responsibility |
|------|---------------|
| `internal/prometheus/queries.go` | Builds safe PromQL query strings |
| `internal/prometheus/client.go`  | Executes queries, parses responses, computes utilization summaries |

## Where the Metrics Come From

KubeVirt exposes VM-level metrics through **virt-handler**, the DaemonSet that runs on every node hosting virtual machine instances (VMIs). Two metrics are central to OVRO:

| Metric | Type | What It Measures |
|--------|------|-----------------|
| `kubevirt_vmi_cpu_usage_seconds_total` | Counter | Cumulative CPU seconds consumed by a VMI. Because it is a monotonically increasing counter, you must apply `rate()` to derive a per-second usage rate. |
| `kubevirt_vmi_memory_resident_bytes` | Gauge | Current resident (RSS) memory of a VMI in bytes. This is an instantaneous value -- no `rate()` needed. |

Both metrics carry `name` and `namespace` labels that identify the specific VMI, which is how OVRO targets a single VM when querying.

## Building Safe PromQL Queries

### The Injection Problem

PromQL queries are strings. When you interpolate user-controlled values -- like a VM name -- directly into a query, you create the same class of vulnerability as SQL injection. A malicious or accidental VM name containing `"` or `\` characters could break the query structure or alter its meaning.

The `sanitizeLabelValue` function addresses this:

```go
func sanitizeLabelValue(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `"`, `\"`)
    s = strings.ReplaceAll(s, "\n", "")
    s = strings.ReplaceAll(s, "\r", "")
    return s
}
```

**What each line does:**

1. **Escape backslashes first** (`\` becomes `\\`). This must happen before escaping quotes, or the backslash inserted by quote-escaping would itself get double-escaped.
2. **Escape double quotes** (`"` becomes `\"`). Label matchers in PromQL use double-quoted strings, so an unescaped quote would terminate the matcher early.
3. **Strip newlines and carriage returns**. These have no legitimate use in a label value and could be used to inject multi-line query fragments.

Every query builder calls `sanitizeLabelValue` on all user-provided inputs before interpolation. This is a defense-in-depth measure -- even if the upstream caller validates names, the query layer does not trust them.

### The CPU Query: `rate()` + Subquery Syntax

```go
func cpuUsageQuery(vmName, namespace, lookback string) string {
    return fmt.Sprintf(
        `rate(kubevirt_vmi_cpu_usage_seconds_total{name="%s",namespace="%s"}[5m])[%s:1m]`,
        sanitizeLabelValue(vmName), sanitizeLabelValue(namespace), lookback,
    )
}
```

This query has two layers, and understanding each is important:

**Inner expression: `rate(...[5m])`**

The `rate()` function computes the per-second average rate of increase of a counter over a time window. The `[5m]` range selector tells Prometheus to look at the last 5 minutes of counter values to compute each rate data point. The result is a floating-point number representing "CPU cores used" (since 1 CPU-second per second = 1 core).

**Outer expression: `[lookback:1m]`**

This is a **subquery**. The syntax `[range:step]` tells Prometheus: "evaluate the inner expression repeatedly, going back `range` in time, producing one data point every `step`." So `[7d:1m]` means "give me the 5-minute rate evaluated once per minute for the past 7 days."

The result is a **matrix** -- a series of `(timestamp, value)` pairs -- which is exactly what OVRO needs to compute percentiles and find peaks.

**Visual breakdown:**

```
rate(kubevirt_vmi_cpu_usage_seconds_total{name="my-vm",namespace="prod"}[5m])[7d:1m]
|    |                                    |                              |    |     |
|    |                                    +-- label matchers             |    |     |
|    +-- counter metric                                                  |    |     |
+-- per-second rate over 5m windows                                      |    |     |
                                                                         |    +-- evaluate every 1 minute
                                                                         +-- over the last 7 days
```

### The Memory Query: Simple Range Vector

```go
func memoryResidentQuery(vmName, namespace, lookback string) string {
    return fmt.Sprintf(
        `kubevirt_vmi_memory_resident_bytes{name="%s",namespace="%s"}[%s]`,
        sanitizeLabelValue(vmName), sanitizeLabelValue(namespace), lookback,
    )
}
```

Memory is a gauge, not a counter, so there is no `rate()` wrapping. The `[lookback]` range selector directly returns all raw samples within the lookback window. Each sample is an absolute byte count -- no derivative needed.

## The Prometheus Client

### Core Types

The client defines a clean type hierarchy that separates concerns:

```go
// Client is an HTTP client for querying Prometheus metrics.
type Client struct {
    baseURL    string
    httpClient *http.Client
}

// VMMetricSample contains time-series values for a single VM metric.
type VMMetricSample struct {
    VMName    string
    Namespace string
    Values    []float64
}

// VMMetrics contains raw CPU and memory samples for a VM.
type VMMetrics struct {
    CPUSamples    []float64
    MemorySamples []float64
}

// VMUtilization contains computed utilization percentages for a VM.
type VMUtilization struct {
    CPUP95Percent    float64
    MemoryP95Percent float64
    CPUMaxPercent    float64
    MemoryMaxPercent float64
}
```

The data flows through three stages:

```
PromQL query --> []VMMetricSample (raw per-metric results)
             --> VMMetrics        (CPU + memory grouped for one VM)
             --> VMUtilization    (statistical summaries ready for the calculator)
```

The `Client` struct is intentionally simple. It holds a base URL (e.g., `https://thanos-querier.openshift-monitoring.svc:9091`) and an `*http.Client` with a 30-second timeout. The `NewClientWithHTTPClient` constructor allows injecting a custom HTTP client for testing or for adding authentication (bearer tokens, TLS client certificates).

### Querying Prometheus: `QueryRange`

Despite its name, `QueryRange` uses Prometheus's **instant query** endpoint (`/api/v1/query`), not the range query endpoint. This is because the subquery syntax in the PromQL expression itself handles the time range, so a single instant evaluation returns the full matrix of historical data points.

```go
func (c *Client) QueryRange(ctx context.Context, query string) ([]VMMetricSample, error) {
    params := url.Values{}
    params.Set("query", query)

    reqURL := fmt.Sprintf("%s/api/v1/query?%s", c.baseURL, params.Encode())
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
    if err != nil {
        return nil, fmt.Errorf("creating request: %w", err)
    }

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("executing query: %w", err)
    }
    defer resp.Body.Close()
```

Key design decisions here:

1. **Context propagation**: The method accepts a `context.Context` so that callers (like a Kubernetes controller reconcile loop) can cancel long-running queries via timeouts or shutdown signals.

2. **URL encoding**: `url.Values.Encode()` safely percent-encodes the query string, preventing URL injection even if the PromQL contains special characters.

### Error Handling with Bounded Reads

When Prometheus returns an error, the response body might be very large (e.g., a verbose HTML error page from a misconfigured proxy). OVRO defends against this:

```go
    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return nil, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, string(body))
    }
```

`io.LimitReader(resp.Body, 4096)` wraps the response body so that at most 4 KB is read into memory. This prevents a misbehaving upstream from causing an out-of-memory condition on the OVRO controller pod. The error message includes both the HTTP status code and the (truncated) body for debugging.

### Parsing the Prometheus Response

The Prometheus HTTP API returns JSON in a specific structure. Here is what a matrix response looks like:

```json
{
  "status": "success",
  "data": {
    "resultType": "matrix",
    "result": [
      {
        "metric": {"name": "my-vm", "namespace": "prod", "__name__": "..."},
        "values": [
          [1714000000, "0.45"],
          [1714000060, "0.52"],
          [1714000120, "0.48"]
        ]
      }
    ]
  }
}
```

Important details about this format:

- Each entry in `values` is a two-element array: `[unix_timestamp, string_value]`.
- **Values are always strings**, even though they represent numbers. This is a Prometheus API convention to preserve floating-point precision. The client must parse them explicitly.
- The `metric` map contains all labels from the original time series.

The internal response struct mirrors this shape:

```go
type prometheusResponse struct {
    Status string `json:"status"`
    Data   struct {
        ResultType string `json:"resultType"`
        Result     []struct {
            Metric map[string]string `json:"metric"`
            Values [][]interface{}   `json:"values"`
        } `json:"result"`
    } `json:"data"`
}
```

Note that `Values` is typed as `[][]interface{}` because each entry is a heterogeneous pair: a number (the timestamp) and a string (the value). The parsing logic extracts what it needs:

```go
    for _, result := range promResp.Data.Result {
        sample := VMMetricSample{
            VMName:    result.Metric["name"],
            Namespace: result.Metric["namespace"],
        }
        for _, v := range result.Values {
            if len(v) >= 2 {
                valStr, ok := v[1].(string)
                if !ok {
                    continue
                }
                val, err := strconv.ParseFloat(valStr, 64)
                if err != nil {
                    continue
                }
                sample.Values = append(sample.Values, val)
            }
        }
        samples = append(samples, sample)
    }
```

The parser is deliberately lenient: if a value cannot be type-asserted to a string or parsed as a float, it is silently skipped. This prevents a single corrupted data point from failing the entire query. The timestamps (`v[0]`) are discarded because the downstream percentile and max calculations only need the values, not when they occurred.

### Fetching Combined Metrics: `GetVMMetrics`

```go
func (c *Client) GetVMMetrics(ctx context.Context, vmName, namespace string, lookbackDays int) (*VMMetrics, error) {
    lookback := fmt.Sprintf("%dd", lookbackDays)

    cpuSamples, err := c.QueryRange(ctx, cpuUsageQuery(vmName, namespace, lookback))
    if err != nil {
        return nil, fmt.Errorf("querying CPU metrics: %w", err)
    }

    memSamples, err := c.QueryRange(ctx, memoryResidentQuery(vmName, namespace, lookback))
    if err != nil {
        return nil, fmt.Errorf("querying memory metrics: %w", err)
    }

    metrics := &VMMetrics{}
    if len(cpuSamples) > 0 {
        metrics.CPUSamples = cpuSamples[0].Values
    }
    if len(memSamples) > 0 {
        metrics.MemorySamples = memSamples[0].Values
    }

    return metrics, nil
}
```

This method is the coordination point. It:

1. Converts the integer `lookbackDays` into a Prometheus duration string (e.g., `7` becomes `"7d"`).
2. Fires two independent queries -- one for CPU, one for memory.
3. Extracts the first result from each (since we filter by a specific VM name + namespace, there should be exactly one matching time series).
4. Packages both into a single `VMMetrics` struct.

The queries run sequentially. Since each query may take several seconds against a large Prometheus instance, a future optimization could run them concurrently using goroutines. However, the sequential approach is simpler and sufficient for the current reconcile-loop pattern where one VM is processed at a time.

### Computing Utilization: `GetVMUtilization`

This is where the medical lab analogy completes -- raw samples become a diagnostic summary:

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
    }

    return utilization, nil
}
```

Two statistics are computed for each resource:

- **P95 (95th percentile)**: The value below which 95% of all samples fall. This filters out brief, extreme spikes and represents "normal peak" behavior. The implementation in `calculator.ComputePercentile` (covered in Chapter 3) uses sorted data with linear interpolation for accuracy.

- **Maximum**: The single highest value observed across the entire lookback window. This captures worst-case scenarios that the P95 would smooth away.

Both statistics are essential for the rightsizing decision: P95 determines the recommended resource level, while the maximum acts as a safety check.

### The `maxValue` Helper

```go
func maxValue(values []float64) float64 {
    if len(values) == 0 {
        return 0
    }
    max := values[0]
    for _, v := range values[1:] {
        if v > max {
            max = v
        }
    }
    return max
}
```

A straightforward linear scan. It returns 0 for empty slices, which is a safe default -- a VM with no data will show 0% utilization and will not trigger a recommendation.

## Putting It All Together

Here is the complete data flow when OVRO evaluates a VM:

```
           +-------------------+
           |  VM Name + NS     |  (from Kubernetes API)
           +--------+----------+
                    |
                    v
     +------------------------------+
     |  sanitizeLabelValue()        |  Escape special characters
     +------------------------------+
                    |
                    v
     +------------------------------+
     |  cpuUsageQuery()             |  Build PromQL with rate() + subquery
     |  memoryResidentQuery()       |  Build PromQL with range selector
     +------------------------------+
                    |
                    v
     +------------------------------+
     |  QueryRange()                |  HTTP GET /api/v1/query
     |  - URL-encode query          |  Parse JSON response
     |  - Handle errors safely      |  Extract float64 values
     +------------------------------+
                    |
                    v
     +------------------------------+
     |  GetVMMetrics()              |  Combine CPU + memory samples
     +------------------------------+
                    |
                    v
     +------------------------------+
     |  GetVMUtilization()          |  Compute P95 and max
     |  - calculator.ComputePercentile
     |  - maxValue()                |
     +------------------------------+
                    |
                    v
           +-------------------+
           |  VMUtilization    |  Ready for the Calculator
           +-------------------+
```

## Key Takeaways

1. **PromQL injection is a real risk.** Any system that builds query strings from external input must sanitize those inputs. The `sanitizeLabelValue` function escapes backslashes, double quotes, and strips newlines to prevent label matcher manipulation.

2. **Counters and gauges require different query strategies.** CPU is a counter and needs `rate()` to derive a usage rate. Memory is a gauge and can be queried directly. Choosing the wrong approach yields meaningless numbers.

3. **Subquery syntax is powerful.** The `[range:step]` suffix lets you turn any instant expression into a historical matrix using a single API call to the instant query endpoint, avoiding the complexity of the range query endpoint with its explicit start/end/step parameters.

4. **Defensive parsing matters.** The Prometheus API returns values as strings in heterogeneous arrays. The parser uses type assertions and `continue` on failure, ensuring that one bad data point does not invalidate an entire result set.

5. **Bounded reads prevent resource exhaustion.** Using `io.LimitReader` on error responses protects the controller from allocating unbounded memory when Prometheus returns an unexpectedly large error body.

6. **Separation of concerns keeps the code testable.** Query building, HTTP transport, JSON parsing, and statistical computation are each isolated. The `NewClientWithHTTPClient` constructor makes it straightforward to inject a test HTTP client.

## Next Chapter

In [Chapter 3: Calculator](03-Calculator.md), we dive into the `calculator` package -- where `ComputePercentile` implements percentile computation with linear interpolation, and the `Analyze` function decides whether a VM should be downsized, upsized, or left alone based on the utilization data we just learned how to collect.
