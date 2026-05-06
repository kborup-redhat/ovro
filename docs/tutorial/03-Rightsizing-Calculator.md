---
title: "Chapter 3: Rightsizing Calculator"
order: 3
---

# Chapter 3: Rightsizing Calculator

In [Chapter 2](./02-Metrics-Collector.md) we saw how the metrics collector gathers CPU and memory utilisation samples from each virtual machine. Raw metrics alone are not actionable, though. Someone -- or something -- needs to look at those numbers and decide: "This VM is wasting resources" or "This VM is running dangerously hot." That is the job of the **rightsizing calculator**.

## What does the calculator do?

The calculator takes a summary of recent resource usage (percentile values, current allocation) together with operator-configured thresholds, and produces a concrete recommendation: **downsize**, **upsize**, or do nothing. It is a pure function with no side effects -- no Kubernetes calls, no network I/O, no state. Given the same input it always returns the same output, which makes it straightforward to test and reason about.

> **Analogy:** Think of the calculator as a financial advisor. It looks at your spending history (metrics), adds a safety buffer so you are never caught short (headroom), and recommends a right-sized budget (resources). Crucially, it only recommends a change when the savings -- or the risk of running out -- are significant enough to justify the disruption (thresholds).

## Source file

All of the code covered in this chapter lives in a single file:

```
internal/calculator/calculator.go
```

Let's walk through it section by section.

---

## 1. Types

### Direction

```go
// Direction indicates whether a VM should be downsized or upsized.
type Direction string

const (
	// Downsize indicates the VM is over-provisioned and should be reduced.
	Downsize Direction = "downsize"
	// Upsize indicates the VM is under-provisioned and should be enlarged.
	Upsize Direction = "upsize"
)
```

`Direction` is a simple string-based enum. There are only two possible recommendations the calculator can make. If neither applies, the calculator returns `nil` (no recommendation at all).

### AnalysisInput

```go
// AnalysisInput contains the metrics and thresholds needed to compute
// a rightsizing recommendation.
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
}
```

This struct bundles everything the calculator needs:

| Field | Purpose |
|-------|---------|
| `CurrentCPUCores` / `CurrentMemoryGiB` | The VM's **current** allocation. |
| `CPUP95Percent` / `MemoryP95Percent` | The 95th-percentile utilisation over the analysis window. |
| `CPUMaxPercent` / `MemoryMaxPercent` | The peak utilisation (reserved for future use / upsize checks). |
| `HeadroomPercent` | Safety buffer added on top of observed usage (e.g., 20 means 20%). |
| `MinCPUSavings` / `MinMemorySavingsGiB` | Minimum savings required before a downsize recommendation is issued. This prevents churn from trivial changes. |
| `UpsizeThresholdPct` | If P95 utilisation exceeds this percentage, the VM is considered under-provisioned. |

### AnalysisResult

```go
// AnalysisResult contains the rightsizing recommendation.
type AnalysisResult struct {
	Direction            Direction
	RecommendedCPUCores  int32
	RecommendedMemoryGiB int32
	CPUSavings           int32
	MemorySavings        int32
}
```

The result carries the direction (downsize or upsize), the recommended new values, and the delta. For a downsize the savings are positive ("you save 3 cores"). For an upsize the savings are negative ("you need 2 more cores"), reflecting that upsizing is a cost increase.

---

## 2. ComputePercentile -- linear interpolation between nearest ranks

Before the calculator can run, something must distil a stream of raw metric samples into a single representative number. That is what `ComputePercentile` does.

```go
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
```

### How it works, step by step

1. **Guard clause.** If the slice is empty, return 0 immediately. There is nothing to calculate.

2. **Non-destructive sort.** The function copies the input into a new slice before sorting so the caller's data is not mutated.

3. **Single-sample shortcut.** If there is exactly one sample, that sample *is* the percentile regardless of which percentile you ask for.

4. **Rank calculation.** The rank is computed as:

   ```
   rank = percentile / 100 * (N - 1)
   ```

   where `N` is the number of samples. This maps the percentile (0-100) onto the zero-based index space of the sorted slice.

5. **Floor and ceil.** The rank is usually not a whole number, so we find the two adjacent indices:
   - `lower = floor(rank)` -- the index just below the rank.
   - `upper = ceil(rank)` -- the index just above the rank.

6. **Exact hit.** If `lower == upper`, the rank lands exactly on an index and we return that value directly.

7. **Linear interpolation.** Otherwise, we compute the fractional distance between the two indices and blend their values:

   ```
   fraction = rank - lower
   result   = sorted[lower] + fraction * (sorted[upper] - sorted[lower])
   ```

### Quick example

Suppose we have 10 sorted samples: `[10, 20, 30, 40, 50, 60, 70, 80, 90, 100]` and we want P95.

```
rank = 95 / 100 * (10 - 1) = 8.55
lower = 8  -> sorted[8] = 90
upper = 9  -> sorted[9] = 100
fraction = 8.55 - 8 = 0.55

result = 90 + 0.55 * (100 - 90) = 90 + 5.5 = 95.5
```

The 95th percentile of this data set is **95.5**, interpolated between the 9th and 10th values.

---

## 3. Analyze -- the entry point

```go
// Analyze evaluates the given metrics and returns a rightsizing recommendation,
// or nil if no action is needed. Upsize is checked first as it is more urgent.
func Analyze(input AnalysisInput) *AnalysisResult {
	// Check for upsize first (more urgent)
	if input.CPUP95Percent >= float64(input.UpsizeThresholdPct) ||
		input.MemoryP95Percent >= float64(input.UpsizeThresholdPct) {
		return analyzeUpsize(input)
	}

	return analyzeDownsize(input)
}
```

The logic here is deliberately simple and reflects an important operational priority: **upsize is always checked first**. A VM that is running out of CPU or memory is at risk of degraded performance or OOM kills right now. A VM that is merely over-provisioned is wasting money, but nothing is on fire. Safety before savings.

The upsize threshold comparison uses an OR -- if *either* CPU or memory P95 exceeds the threshold, the VM is considered under-provisioned and we branch into `analyzeUpsize`. Otherwise, we fall through to `analyzeDownsize`.

---

## 4. analyzeDownsize -- recommending a smaller VM

```go
func analyzeDownsize(input AnalysisInput) *AnalysisResult {
	headroomMultiplier := 1.0 + float64(input.HeadroomPercent)/100.0

	cpuUsageCores := float64(input.CurrentCPUCores) * input.CPUP95Percent / 100.0
	recommendedCPU := int32(math.Ceil(cpuUsageCores * headroomMultiplier))

	memUsageGiB := float64(input.CurrentMemoryGiB) * input.MemoryP95Percent / 100.0
	recommendedMem := int32(math.Ceil(memUsageGiB * headroomMultiplier))

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
	}
}
```

### Step-by-step breakdown

**1. Headroom multiplier**

```go
headroomMultiplier := 1.0 + float64(input.HeadroomPercent)/100.0
```

If `HeadroomPercent` is 20, the multiplier becomes `1.20`. This means the recommended allocation will be 20% larger than observed usage, providing a safety buffer for traffic spikes.

**2. Convert percentile usage to absolute cores / GiB**

```go
cpuUsageCores := float64(input.CurrentCPUCores) * input.CPUP95Percent / 100.0
```

The P95 value is a percentage of the current allocation. Multiplying by the current allocation converts it to absolute units. For example, if a VM has 8 cores and P95 CPU is 28.3%, then `cpuUsageCores = 8 * 28.3 / 100 = 2.264`.

**3. Apply headroom and round up**

```go
recommendedCPU := int32(math.Ceil(cpuUsageCores * headroomMultiplier))
```

Multiply observed usage by the headroom multiplier and round up to the next whole core (you cannot allocate 2.7 cores). Using `math.Ceil` ensures we never round *down* and under-provision.

**4. Cap at current allocation**

```go
if recommendedCPU >= input.CurrentCPUCores {
    recommendedCPU = input.CurrentCPUCores
}
```

If after adding headroom the recommendation is equal to or greater than the current allocation, there is no point downsizing. We cap at the current value so savings come out to zero.

**5. Check minimum savings threshold (strict less-than)**

```go
if cpuSavings < input.MinCPUSavings || memSavings < input.MinMemorySavingsGiB {
    return nil
}
```

This is the "is it worth the disruption?" check. Notice it uses strict less-than (`<`), not less-than-or-equal. This means if `MinCPUSavings` is 1 and `cpuSavings` is exactly 1, the savings *do* meet the threshold and the recommendation proceeds.

Also notice it uses OR (`||`): **both** CPU and memory must meet their respective thresholds. If either one falls short, the entire recommendation is suppressed. This prevents scenarios where we downsize CPU but leave memory unchanged (or vice versa) -- the recommendation is all-or-nothing.

---

## 5. analyzeUpsize -- recommending a larger VM

```go
func analyzeUpsize(input AnalysisInput) *AnalysisResult {
	cpuUsageCores := float64(input.CurrentCPUCores) * input.CPUP95Percent / 100.0
	memUsageGiB := float64(input.CurrentMemoryGiB) * input.MemoryP95Percent / 100.0

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

	return &AnalysisResult{
		Direction:            Upsize,
		RecommendedCPUCores:  recommendedCPU,
		RecommendedMemoryGiB: recommendedMem,
		CPUSavings:           -cpuIncrease,
		MemorySavings:        -memIncrease,
	}
}
```

### How upsize differs from downsize

**1. Target 70% utilisation (divide by 0.70)**

Instead of applying a headroom *multiplier*, the upsize path targets a specific utilisation percentage. Dividing by 0.70 says: "Size the VM so that the observed P95 usage would only be 70% of the new allocation." This leaves a 30% buffer for spikes.

```go
recommendedCPU := int32(math.Ceil(cpuUsageCores / 0.70))
```

Why divide instead of multiply? When downsizing we start from *usage* and add a buffer. When upsizing we start from *usage* and solve for "what allocation makes this usage equal to 70%?" -- that's `allocation = usage / 0.70`.

**2. Floor at current allocation**

```go
if recommendedCPU <= input.CurrentCPUCores {
    recommendedCPU = input.CurrentCPUCores
}
```

The opposite of the downsize cap. If the formula somehow recommends fewer resources than already allocated (perhaps only memory is the bottleneck), we floor at the current value so we never *shrink* during an upsize.

**3. Check minimum increase threshold**

```go
if cpuIncrease < input.MinCPUSavings || memIncrease < input.MinMemorySavingsGiB {
    return nil
}
```

The same thresholds apply in both directions. A recommendation to add just 1 core when the minimum is 2 is suppressed.

**4. Negative savings**

```go
CPUSavings:  -cpuIncrease,
MemorySavings: -memIncrease,
```

By convention, the `CPUSavings` and `MemorySavings` fields are negative for upsize recommendations. This makes it easy for downstream consumers to display the cost impact consistently: positive means savings, negative means additional spend.

---

## Worked example

Let's trace through a concrete scenario to see everything working together.

### Inputs

| Parameter | Value |
|-----------|-------|
| Current CPU | 8 cores |
| Current Memory | 16 GiB |
| CPU P95 | 28.3% |
| Memory P95 | 41.7% |
| Headroom | 20% |
| Min CPU Savings | 1 core |
| Min Memory Savings | 1 GiB |
| Upsize Threshold | 80% |

### Step 1: Analyze -- upsize or downsize?

```
CPU P95 (28.3%) >= Upsize Threshold (80)?  No.
Mem P95 (41.7%) >= Upsize Threshold (80)?  No.
```

Neither resource is above the upsize threshold, so we proceed to **analyzeDownsize**.

### Step 2: Headroom multiplier

```
headroomMultiplier = 1.0 + 20/100 = 1.20
```

### Step 3: CPU calculation

```
cpuUsageCores  = 8 * 28.3 / 100 = 2.264 cores
recommendedCPU = ceil(2.264 * 1.20) = ceil(2.7168) = 3 cores
```

The VM is using roughly 2.3 cores at the 95th percentile. Adding 20% headroom brings that to about 2.7 cores, which rounds up to **3 cores**.

### Step 4: Memory calculation

```
memUsageGiB    = 16 * 41.7 / 100 = 6.672 GiB
recommendedMem = ceil(6.672 * 1.20) = ceil(8.0064) = 9 GiB
```

The VM uses about 6.7 GiB at P95. With 20% headroom that becomes about 8.0 GiB, which rounds up to **9 GiB**.

### Step 5: Cap check

```
recommendedCPU (3) >= currentCPU (8)?  No, keep 3.
recommendedMem (9) >= currentMem (16)? No, keep 9.
```

Both recommended values are well below current allocation, so no capping is needed.

### Step 6: Savings

```
cpuSavings = 8 - 3 = 5 cores
memSavings = 16 - 9 = 7 GiB
```

### Step 7: Threshold check

```
cpuSavings (5) < MinCPUSavings (1)?    No, 5 >= 1. Passes.
memSavings (7) < MinMemSavingsGiB (1)? No, 7 >= 1. Passes.
```

Both savings exceed the minimum thresholds, so a recommendation is issued.

### Result

```go
&AnalysisResult{
    Direction:            "downsize",
    RecommendedCPUCores:  3,
    RecommendedMemoryGiB: 9,
    CPUSavings:           5,
    MemorySavings:        7,
}
```

**Verdict:** This VM is significantly over-provisioned. The calculator recommends cutting CPU from 8 to 3 cores (saving 5) and memory from 16 to 9 GiB (saving 7). The 20% headroom ensures the VM still has breathing room above its 95th-percentile load.

---

## Design decisions worth noting

### Why P95 instead of average or max?

- **Average** would ignore spikes entirely, leading to under-provisioning.
- **Max** would be driven by rare outliers, leading to over-provisioning.
- **P95** captures "the load level you experience 95% of the time" -- a practical balance between safety and savings.

### Why are the thresholds AND-gated (using OR to return nil)?

The condition `cpuSavings < min || memSavings < min` returns nil if *either* resource falls short. This ensures that every recommendation is meaningful in *both* dimensions. A recommendation to save 5 CPU cores but 0 GiB of memory would leave the operator in an awkward half-optimised state.

### Why is the function pure?

By taking all inputs as a struct and returning a result with no side effects, the calculator is:
- **Easy to unit test** -- no mocks needed.
- **Easy to reason about** -- same input always produces the same output.
- **Reusable** -- it could be called from a CLI tool, a webhook, or a controller without modification.

---

## Key takeaways

1. **The calculator is a pure function.** It takes metrics and thresholds in, and returns a recommendation out. No Kubernetes calls, no side effects.

2. **Upsize is checked first** because resource exhaustion is more urgent than waste.

3. **Downsize** converts P95 utilisation to absolute units, adds a configurable headroom buffer, rounds up, and caps at the current allocation.

4. **Upsize** targets 70% utilisation by dividing observed usage by 0.70, floors at the current allocation, and reports savings as negative values.

5. **Minimum savings thresholds** prevent churn from trivial changes. Both CPU and memory must independently meet their thresholds for a recommendation to be issued.

6. **ComputePercentile** uses linear interpolation between nearest ranks, giving a smooth percentile value even with sparse data.

---

## Next chapter

The calculator produces a recommendation, but nothing happens until someone acts on it. In [Chapter 4: Recommendation Controller](./04-Recommendation-Controller.md), we will see how the Kubernetes controller calls `Analyze`, creates or updates `RightsizingRecommendation` custom resources, and feeds the results into the operator's reconciliation loop.
