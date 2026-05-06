package calculator_test

import (
	"testing"

	"github.com/kborup-redhat/ovro/internal/calculator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeP95(t *testing.T) {
	samples := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 5, 15, 25, 35, 45, 55, 65, 75, 85, 95}
	p95 := calculator.ComputePercentile(samples, 95)
	assert.InDelta(t, 96.25, p95, 1.0)
}

func TestComputeP95_SingleValue(t *testing.T) {
	samples := []float64{42.0}
	p95 := calculator.ComputePercentile(samples, 95)
	assert.InDelta(t, 42.0, p95, 0.01)
}

func TestComputeP95_Empty(t *testing.T) {
	samples := []float64{}
	p95 := calculator.ComputePercentile(samples, 95)
	assert.Equal(t, 0.0, p95)
}

func TestRecommendDownsize(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     8,
		CurrentMemoryGiB:    16,
		CPUP95Percent:       28.3,
		MemoryP95Percent:    41.7,
		CPUMaxPercent:       62.1,
		MemoryMaxPercent:    58.4,
		HeadroomPercent:     20,
		MinCPUSavings:       1,
		MinMemorySavingsGiB: 1,
		UpsizeThresholdPct:  90,
	}

	result := calculator.Analyze(input)

	require.NotNil(t, result)
	assert.Equal(t, calculator.Downsize, result.Direction)
	// P95 CPU = 28.3% of 8 cores = 2.264 cores. *1.20 = 2.717 → ceil = 3 cores
	assert.Equal(t, int32(3), result.RecommendedCPUCores)
	// P95 Mem = 41.7% of 16 GiB = 6.672 GiB. *1.20 = 8.006 → ceil = 9 GiB
	assert.Equal(t, int32(9), result.RecommendedMemoryGiB)
	// Savings: 8-3=5 cores, 16-9=7 GiB — both > 1, so recommendation generated
	assert.Equal(t, int32(5), result.CPUSavings)
	assert.Equal(t, int32(7), result.MemorySavings)
}

func TestRecommendDownsize_BelowThreshold(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     2,
		CurrentMemoryGiB:    4,
		CPUP95Percent:       60.0,
		MemoryP95Percent:    70.0,
		CPUMaxPercent:       80.0,
		MemoryMaxPercent:    85.0,
		HeadroomPercent:     20,
		MinCPUSavings:       1,
		MinMemorySavingsGiB: 1,
		UpsizeThresholdPct:  90,
	}

	result := calculator.Analyze(input)

	// Savings too small (< 1 CPU or < 1 GiB), no recommendation
	assert.Nil(t, result)
}

func TestRecommendUpsize(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     4,
		CurrentMemoryGiB:    8,
		CPUP95Percent:       94.0,
		MemoryP95Percent:    92.0,
		CPUMaxPercent:       99.0,
		MemoryMaxPercent:    97.0,
		HeadroomPercent:     20,
		MinCPUSavings:       1,
		MinMemorySavingsGiB: 1,
		UpsizeThresholdPct:  90,
	}

	result := calculator.Analyze(input)

	require.NotNil(t, result)
	assert.Equal(t, calculator.Upsize, result.Direction)
	// CPU: P95 usage = 94% of 4 = 3.76 cores. Target = 3.76/0.70 = 5.37 → ceil = 6
	assert.Equal(t, int32(6), result.RecommendedCPUCores)
	// Mem: P95 usage = 92% of 8 = 7.36 GiB. Target = 7.36/0.70 = 10.51 → ceil = 11
	assert.Equal(t, int32(11), result.RecommendedMemoryGiB)
}

func TestRecommendUpsize_BelowThreshold(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     4,
		CurrentMemoryGiB:    8,
		CPUP95Percent:       91.0,
		MemoryP95Percent:    91.0,
		CPUMaxPercent:       95.0,
		MemoryMaxPercent:    95.0,
		HeadroomPercent:     20,
		MinCPUSavings:       1,
		MinMemorySavingsGiB: 1,
		UpsizeThresholdPct:  90,
	}

	result := calculator.Analyze(input)

	// CPU: 91% of 4 = 3.64. Target = 3.64/0.70 = 5.2 → ceil = 6. Increase = 2 cores (>1 ✓)
	// Mem: 91% of 8 = 7.28. Target = 7.28/0.70 = 10.4 → ceil = 11. Increase = 3 GiB (>1 ✓)
	// Both pass threshold, so recommendation IS generated
	require.NotNil(t, result)
	assert.Equal(t, calculator.Upsize, result.Direction)
}

func TestRecommendDownsize_ExactThreshold(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     4,
		CurrentMemoryGiB:    4,
		CPUP95Percent:       30.0,
		MemoryP95Percent:    30.0,
		CPUMaxPercent:       50.0,
		MemoryMaxPercent:    50.0,
		HeadroomPercent:     20,
		MinCPUSavings:       2,
		MinMemorySavingsGiB: 2,
		UpsizeThresholdPct:  90,
	}

	result := calculator.Analyze(input)

	// CPU: 30% of 4 = 1.2. *1.20 = 1.44 → ceil = 2. Savings = 4-2 = 2 (== MinCPUSavings)
	// Mem: same. Savings = 2 (== MinMemorySavingsGiB)
	// Exact threshold match should produce a recommendation
	require.NotNil(t, result)
	assert.Equal(t, calculator.Downsize, result.Direction)
	assert.Equal(t, int32(2), result.CPUSavings)
	assert.Equal(t, int32(2), result.MemorySavings)
}

func TestNoRecommendation_NormalUtilization(t *testing.T) {
	input := calculator.AnalysisInput{
		CurrentCPUCores:     4,
		CurrentMemoryGiB:    8,
		CPUP95Percent:       75.0,
		MemoryP95Percent:    75.0,
		CPUMaxPercent:       85.0,
		MemoryMaxPercent:    85.0,
		HeadroomPercent:     20,
		MinCPUSavings:       1,
		MinMemorySavingsGiB: 1,
		UpsizeThresholdPct:  90,
	}

	// CPU: 75% of 4 = 3.0, *1.20 = 3.6 → ceil = 4. Savings = 0.
	// Mem: 75% of 8 = 6.0, *1.20 = 7.2 → ceil = 8. Savings = 0.
	// No savings, no recommendation.
	result := calculator.Analyze(input)
	assert.Nil(t, result)
}
