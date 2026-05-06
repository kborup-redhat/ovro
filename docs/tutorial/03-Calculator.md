---
title: "Chapter 3: Calculator"
order: 3
---

# Chapter 3: Calculator

## Introduction

The Calculator is the brain of OVRO — a pure function that takes utilisation metrics and policy thresholds as input and outputs a rightsizing recommendation (or nil if no action is needed). It has no dependencies on Kubernetes, Prometheus, or any I/O, making it easy to test and reason about. Think of it as a formula in a spreadsheet: given these inputs, here's the answer.

## How It Works

The calculator performs two checks in priority order:

1. **Upsize check** (higher priority): Is the VM under-provisioned? If P95 CPU or memory exceeds the upsize threshold (default 90%), recommend increasing resources.
2. **Downsize check**: Is the VM over-provisioned? If there are sufficient savings after applying headroom, recommend reducing resources.

```go
// internal/calculator/calculator.go

func Analyze(input AnalysisInput) *AnalysisResult {
    if input.CPUP95Percent >= float64(input.UpsizeThresholdPct) ||
        input.MemoryP95Percent >= float64(input.UpsizeThresholdPct) {
        return analyzeUpsize(input)
    }
    return analyzeDownsize(input)
}
```

Upsize takes priority because an under-provisioned VM is more urgent (it may be experiencing performance degradation) than an over-provisioned one (which is just wasting resources).

## Downsize Calculation

```go
func analyzeDownsize(input AnalysisInput) *AnalysisResult {
    headroomMultiplier := 1.0 + float64(input.HeadroomPercent)/100.0

    cpuUsageCores := float64(input.CurrentCPUCores) * input.CPUP95Percent / 100.0
    recommendedCPU := int32(math.Ceil(cpuUsageCores * headroomMultiplier))

    memUsageGiB := float64(input.CurrentMemoryGiB) * input.MemoryP95Percent / 100.0
    recommendedMem := int32(math.Ceil(memUsageGiB * headroomMultiplier))

    // Clamp minimums
    if recommendedCPU < 1 { recommendedCPU = 1 }
    if recommendedMem < 1 { recommendedMem = 1 }

    // Don't recommend increasing resources in downsize mode
    if recommendedCPU >= input.CurrentCPUCores { recommendedCPU = input.CurrentCPUCores }
    if recommendedMem >= input.CurrentMemoryGiB { recommendedMem = input.CurrentMemoryGiB }

    cpuSavings := input.CurrentCPUCores - recommendedCPU
    memSavings := input.CurrentMemoryGiB - recommendedMem

    // Only recommend if savings meet the minimum threshold
    if cpuSavings < input.MinCPUSavings || memSavings < input.MinMemorySavingsGiB {
        return nil
    }

    return &AnalysisResult{
        Direction:            Downsize,
        RecommendedCPUCores:  recommendedCPU,
        RecommendedMemoryGiB: recommendedMem,
        CPUSavings:           cpuSavings,
        MemorySavings:        memSavings,
    }
}
```

The formula is:

```
recommended = ceil(current * P95_percent/100 * (1 + headroom/100))
```

For example, a VM with 8 CPU cores, P95 utilisation of 25%, and 20% headroom:
- Usage: 8 * 0.25 = 2.0 cores
- With headroom: 2.0 * 1.20 = 2.4
- Rounded up: 3 cores
- Savings: 8 - 3 = 5 cores

The minimum savings threshold prevents trivial recommendations (e.g., going from 4 to 3 cores when the threshold is 1).

## Upsize Calculation

```go
func analyzeUpsize(input AnalysisInput) *AnalysisResult {
    recommendedCPU := int32(math.Ceil(cpuUsageCores / 0.70))
    recommendedMem := int32(math.Ceil(memUsageGiB / 0.70))
    // ...
}
```

Upsizing targets 70% utilisation as the comfortable operating point. If a VM's P95 CPU is at 92% of 4 cores (3.68 cores), the recommended size is `ceil(3.68 / 0.70)` = 6 cores.

## Percentile Computation

The calculator also provides a general-purpose percentile function used by the Prometheus client:

```go
func ComputePercentile(samples []float64, percentile int) float64 {
    sorted := make([]float64, len(samples))
    copy(sorted, samples)
    sort.Float64s(sorted)

    rank := float64(percentile) / 100.0 * float64(len(sorted)-1)
    lower := int(math.Floor(rank))
    upper := int(math.Ceil(rank))
    if lower == upper {
        return sorted[lower]
    }
    fraction := rank - float64(lower)
    return sorted[lower] + fraction*(sorted[upper]-sorted[lower])
}
```

This uses linear interpolation between the two nearest ranks, matching the standard statistical definition of percentile.

## Key Takeaways

- The calculator is a pure function: no I/O, no state, easy to test.
- Upsize is checked before downsize (urgency-first).
- Headroom percent provides a safety buffer above the observed percentile.
- Minimum savings thresholds prevent trivial recommendations.
- A nil return means "no action needed" — the VM is right-sized.

## Next Steps

The calculator produces the analysis, but it doesn't know about Kubernetes. Next, we'll see how the Recommendation Controller ties everything together: watching VMs, fetching metrics, running the calculator, and persisting results as CRs.
