package prometheus_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kborup-redhat/ovro/internal/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryVMMetrics_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		var resp map[string]interface{}

		if query != "" {
			resp = map[string]interface{}{
				"status": "success",
				"data": map[string]interface{}{
					"resultType": "matrix",
					"result": []interface{}{
						map[string]interface{}{
							"metric": map[string]string{
								"name":      "test-vm",
								"namespace": "default",
							},
							"values": []interface{}{
								[]interface{}{1620000000.0, "0.25"},
								[]interface{}{1620000060.0, "0.30"},
								[]interface{}{1620000120.0, "0.28"},
							},
						},
					},
				},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	samples, err := client.QueryRange(context.Background(), "test_metric")

	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, "test-vm", samples[0].VMName)
	assert.Equal(t, "default", samples[0].Namespace)
	assert.Len(t, samples[0].Values, 3)
}

func TestQueryVMMetrics_EmptyResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "matrix",
				"result":     []interface{}{},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	samples, err := client.QueryRange(context.Background(), "test_metric")

	require.NoError(t, err)
	assert.Empty(t, samples)
}

func TestQueryVMMetrics_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	_, err := client.QueryRange(context.Background(), "test_metric")

	assert.Error(t, err)
}

func TestGetVMCPUMemoryMetrics(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "matrix",
				"result": []interface{}{
					map[string]interface{}{
						"metric": map[string]string{
							"name":      "test-vm",
							"namespace": "default",
						},
						"values": []interface{}{
							[]interface{}{1620000000.0, "50.0"},
							[]interface{}{1620000060.0, "60.0"},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	metrics, err := client.GetVMMetrics(context.Background(), "test-vm", "default", 14)

	require.NoError(t, err)
	assert.NotEmpty(t, metrics.CPUSamples)
	assert.NotEmpty(t, metrics.MemorySamples)
}

func TestGetVMUtilization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "matrix",
				"result": []interface{}{
					map[string]interface{}{
						"metric": map[string]string{
							"name":      "test-vm",
							"namespace": "default",
						},
						"values": []interface{}{
							[]interface{}{1620000000.0, "10.0"},
							[]interface{}{1620000060.0, "20.0"},
							[]interface{}{1620000120.0, "30.0"},
							[]interface{}{1620000180.0, "40.0"},
							[]interface{}{1620000240.0, "50.0"},
							[]interface{}{1620000300.0, "60.0"},
							[]interface{}{1620000360.0, "70.0"},
							[]interface{}{1620000420.0, "80.0"},
							[]interface{}{1620000480.0, "90.0"},
							[]interface{}{1620000540.0, "100.0"},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	util, err := client.GetVMUtilization(context.Background(), "test-vm", "default", 14)

	require.NoError(t, err)
	assert.Greater(t, util.CPUP95Percent, 0.0)
	assert.Greater(t, util.MemoryP95Percent, 0.0)
	assert.Equal(t, 100.0, util.CPUMaxPercent)
	assert.Equal(t, 100.0, util.MemoryMaxPercent)
}

func TestGetVMUtilization_EmptyMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status": "success",
			"data": map[string]interface{}{
				"resultType": "matrix",
				"result":     []interface{}{},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := prometheus.NewClient(server.URL)
	util, err := client.GetVMUtilization(context.Background(), "test-vm", "default", 14)

	require.NoError(t, err)
	assert.Equal(t, 0.0, util.CPUP95Percent)
	assert.Equal(t, 0.0, util.MemoryP95Percent)
	assert.Equal(t, 0.0, util.CPUMaxPercent)
	assert.Equal(t, 0.0, util.MemoryMaxPercent)
}
