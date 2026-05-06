package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kborup-redhat/ovro/internal/calculator"
)

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

// NewClient creates a new Prometheus client with the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithHTTPClient creates a new Prometheus client with a custom HTTP client.
func NewClientWithHTTPClient(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

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

// QueryRange executes a Prometheus instant query and returns the metric samples.
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("prometheus returned status %d: %s", resp.StatusCode, string(body))
	}

	var promResp prometheusResponse
	if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var samples []VMMetricSample
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

	return samples, nil
}

// GetVMMetrics fetches both CPU and memory metrics for a specific VM.
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

// GetVMUtilization fetches VM metrics and computes P95 and max utilization percentages.
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

// maxValue returns the maximum value from a slice of float64.
// Returns 0 if the slice is empty.
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
