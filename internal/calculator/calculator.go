package calculator

import (
	"fmt"
	"math"
	"sort"
)

// Direction indicates whether a VM should be downsized or upsized.
type Direction string

const (
	// Downsize indicates the VM is over-provisioned and should be reduced.
	Downsize Direction = "downsize"
	// Upsize indicates the VM is under-provisioned and should be enlarged.
	Upsize Direction = "upsize"
)

// AnalysisInput contains the metrics and thresholds needed to compute a rightsizing recommendation.
type AnalysisInput struct {
	CurrentCPUCores     int32
	CurrentMemoryGiB    int32
	CPUP95Percent       float64
	MemoryP95Percent    float64
	CPUMaxPercent       float64
	MemoryMaxPercent    float64
	HeadroomPercent     int
	MinCPUSavings       int32
	MinMemorySavingsGiB int32
	UpsizeThresholdPct  int
	LookbackDays        int
}

// AnalysisResult contains the rightsizing recommendation.
type AnalysisResult struct {
	Direction            Direction
	RecommendedCPUCores  int32
	RecommendedMemoryGiB int32
	CPUSavings           int32
	MemorySavings        int32
	Reason               string
}

// ComputePercentile calculates the given percentile from a slice of samples
// using linear interpolation between the two nearest ranks.
// Returns 0 if samples is empty.
func ComputePercentile(samples []float64, percentile int) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	if len(sorted) == 1 {
		return sorted[0]
	}

	rank := float64(percentile) / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	fraction := rank - float64(lower)
	return sorted[lower] + fraction*(sorted[upper]-sorted[lower])
}

// Analyze evaluates the given metrics and returns a rightsizing recommendation,
// or nil if no action is needed. Upsize is checked first as it is more urgent.
func Analyze(input AnalysisInput) *AnalysisResult {
	threshold := float64(input.UpsizeThresholdPct)

	if input.CPUP95Percent >= threshold || input.MemoryP95Percent >= threshold ||
		input.CPUMaxPercent >= threshold || input.MemoryMaxPercent >= threshold {
		return analyzeUpsize(input)
	}

	return analyzeDownsize(input)
}

func analyzeDownsize(input AnalysisInput) *AnalysisResult {
	headroomMultiplier := 1.0 + float64(input.HeadroomPercent)/100.0

	cpuUsageCores := float64(input.CurrentCPUCores) * input.CPUP95Percent / 100.0
	recommendedCPU := int32(math.Ceil(cpuUsageCores * headroomMultiplier))

	memUsageGiB := float64(input.CurrentMemoryGiB) * input.MemoryP95Percent / 100.0
	recommendedMem := int32(math.Ceil(memUsageGiB * headroomMultiplier))

	if recommendedCPU < 1 {
		recommendedCPU = 1
	}
	if recommendedMem < 1 {
		recommendedMem = 1
	}

	if recommendedCPU >= input.CurrentCPUCores {
		recommendedCPU = input.CurrentCPUCores
	}
	if recommendedMem >= input.CurrentMemoryGiB {
		recommendedMem = input.CurrentMemoryGiB
	}

	cpuSavings := input.CurrentCPUCores - recommendedCPU
	memSavings := input.CurrentMemoryGiB - recommendedMem

	if cpuSavings < input.MinCPUSavings || memSavings < input.MinMemorySavingsGiB {
		return nil
	}

	return &AnalysisResult{
		Direction:            Downsize,
		RecommendedCPUCores:  recommendedCPU,
		RecommendedMemoryGiB: recommendedMem,
		CPUSavings:           cpuSavings,
		MemorySavings:        memSavings,
		Reason: fmt.Sprintf("CPU only %.1f%% utilized (P95 over %dd), can save %d cores",
			input.CPUP95Percent, input.LookbackDays, cpuSavings),
	}
}

func analyzeUpsize(input AnalysisInput) *AnalysisResult {
	cpuPct := math.Max(input.CPUP95Percent, input.CPUMaxPercent)
	memPct := math.Max(input.MemoryP95Percent, input.MemoryMaxPercent)

	cpuUsageCores := float64(input.CurrentCPUCores) * cpuPct / 100.0
	memUsageGiB := float64(input.CurrentMemoryGiB) * memPct / 100.0

	recommendedCPU := int32(math.Ceil(cpuUsageCores / 0.70))
	recommendedMem := int32(math.Ceil(memUsageGiB / 0.70))

	if recommendedCPU <= input.CurrentCPUCores {
		recommendedCPU = input.CurrentCPUCores
	}
	if recommendedMem <= input.CurrentMemoryGiB {
		recommendedMem = input.CurrentMemoryGiB
	}

	cpuIncrease := recommendedCPU - input.CurrentCPUCores
	memIncrease := recommendedMem - input.CurrentMemoryGiB

	if cpuIncrease < input.MinCPUSavings || memIncrease < input.MinMemorySavingsGiB {
		return nil
	}

	threshold := float64(input.UpsizeThresholdPct)
	spikeTriggered := (input.CPUMaxPercent >= threshold && input.CPUP95Percent < threshold) ||
		(input.MemoryMaxPercent >= threshold && input.MemoryP95Percent < threshold)

	var reason string
	if spikeTriggered {
		reason = fmt.Sprintf("CPU spikes to %.1f%% (sustained P95: %.1f%% over %dd)",
			input.CPUMaxPercent, input.CPUP95Percent, input.LookbackDays)
	} else {
		reason = fmt.Sprintf("CPU at %.1f%% sustained utilization (P95 over %dd)",
			input.CPUP95Percent, input.LookbackDays)
	}

	return &AnalysisResult{
		Direction:            Upsize,
		RecommendedCPUCores:  recommendedCPU,
		RecommendedMemoryGiB: recommendedMem,
		CPUSavings:           -cpuIncrease,
		MemorySavings:        -memIncrease,
		Reason:               reason,
	}
}
