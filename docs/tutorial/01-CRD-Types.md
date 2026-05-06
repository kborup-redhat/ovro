---
title: "Chapter 1: CRD Types"
order: 1
---

# Chapter 1: CRD Types

In this chapter, we introduce the two Custom Resource Definitions (CRDs) that form the foundation of the OVRO project. By the end, you will understand what each CRD represents, how they are structured, and how they work together to drive the VM rightsizing workflow.

## What Are CRDs?

Kubernetes ships with built-in resource types like Pods, Services, and Deployments. A **Custom Resource Definition** (CRD) lets you extend Kubernetes with your own resource types that behave exactly like the built-in ones -- they are stored in etcd, exposed through the Kubernetes API, and can be watched, listed, created, updated, and deleted with `oc` / `kubectl`.

Think of CRDs as **your own custom database tables in Kubernetes**. When you define a CRD, you are telling Kubernetes: "I want a new table with these columns, and I want the API server to manage it for me." Once the CRD is installed, you can create instances of that resource (called Custom Resources, or CRs), and controllers can watch for changes and act on them.

OVRO defines two CRDs:

| CRD | Scope | Purpose |
|---|---|---|
| `RightsizingRecommendation` | Namespaced | One per VM, carries the diagnosis, treatment, and status |
| `RightsizingPolicy` | Cluster | Singleton, configures algorithm parameters and operational policy |

## The Prescription Analogy

Throughout this chapter, we use a medical analogy to make the concepts concrete:

- A **RightsizingRecommendation** is like a **doctor's prescription**. It describes the **diagnosis** (current metrics showing the VM is over- or under-provisioned), the **treatment** (recommended CPU and memory changes), the **expected savings**, and tracks whether the prescription was **filled** (applied to the VM).

- A **RightsizingPolicy** is like the **hospital's treatment guidelines**. It does not describe any individual patient -- it sets the rules that all doctors follow: which percentile of metrics to use, how much headroom to leave, what the minimum savings threshold is before issuing a recommendation, and whether prescriptions can be auto-filled without manual approval.

## RightsizingRecommendation

**Source file:** `api/v1alpha1/rightsizingrecommendation_types.go`

This CRD is namespaced (one recommendation lives in the same namespace as the VM it targets) and captures everything about a single rightsizing recommendation.

### ObjectRef -- Which VM?

Every recommendation points at exactly one VirtualMachine:

```go
type ObjectRef struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
}
```

This reference appears in the spec as `VirtualMachineRef`, linking the recommendation to the VM it was generated for.

### Direction -- Downsize or Upsize?

A recommendation has a **direction** that tells you whether the VM needs fewer resources or more:

```go
type RecommendationDirection string

const (
    DirectionDownsize RecommendationDirection = "downsize"
    DirectionUpsize   RecommendationDirection = "upsize"
)
```

- **downsize** -- the VM is over-provisioned; reducing resources saves cost without impacting performance.
- **upsize** -- the VM is under-provisioned; it is hitting resource ceilings and needs more headroom.

### ResourceSpec -- CPU and Memory

Both the current allocation and the recommended allocation use the same `ResourceSpec` structure:

```go
type CPUSpec struct {
    Cores   int32 `json:"cores"`
    Sockets int32 `json:"sockets"`
    Threads int32 `json:"threads"`
}

type ResourceSpec struct {
    CPU    CPUSpec           `json:"cpu"`
    Memory resource.Quantity `json:"memory"`
}
```

CPU is expressed in the KubeVirt topology format (cores, sockets, threads) rather than as a flat millicore value. This matters because VM CPU topology affects guest OS behavior, NUMA alignment, and license compliance. Memory uses the standard Kubernetes `resource.Quantity` type (e.g., `"4Gi"`).

The spec carries both `Current` and `Recommended` fields of this type, so you can see exactly what changed:

```go
type RightsizingRecommendationSpec struct {
    VirtualMachineRef ObjectRef               `json:"virtualMachineRef"`
    Direction         RecommendationDirection `json:"direction"`
    Current           ResourceSpec            `json:"current"`
    Recommended       ResourceSpec            `json:"recommended"`
    Savings           SavingsSpec             `json:"savings"`
    Metrics           MetricsSnapshot         `json:"metrics"`
    HotplugCapable    bool                    `json:"hotplugCapable"`
}
```

The `HotplugCapable` boolean indicates whether the VM supports live resource changes (CPU/memory hotplug). When true, certain changes can be applied without a VM restart.

### MetricsSnapshot -- The Evidence

Every prescription needs a diagnosis. The `MetricsSnapshot` captures the utilization data that justified the recommendation:

```go
type MetricsSnapshot struct {
    LookbackDays     int     `json:"lookbackDays"`
    CPUP95Percent    float64 `json:"cpuP95Percent"`
    MemoryP95Percent float64 `json:"memoryP95Percent"`
    CPUMaxPercent    float64 `json:"cpuMaxPercent"`
    MemoryMaxPercent float64 `json:"memoryMaxPercent"`
}
```

This stores both **P95** (95th percentile) and **max** utilization for CPU and memory over the lookback window. P95 represents the "normal peak" -- the level that covers 95% of all observed data points. The max captures the absolute highest spike. Together, they give a complete picture: P95 tells you what the workload typically needs, and max tells you the worst case.

### SavingsSpec -- The Payoff

For downsize recommendations, the savings show how much resource can be reclaimed:

```go
type SavingsSpec struct {
    CPU    int32             `json:"cpu"`
    Memory resource.Quantity `json:"memory"`
}
```

CPU savings are expressed as a count of cores freed, and memory savings as a `resource.Quantity`. These values are useful for reporting and for setting minimum thresholds in the policy (do not bother with a recommendation that only saves 0.5 cores).

### Status and the State Machine

The status subresource tracks where the recommendation is in its lifecycle:

```go
type RightsizingRecommendationStatus struct {
    State              RecommendationState `json:"state,omitempty"`
    LastCalculated     *metav1.Time        `json:"lastCalculated,omitempty"`
    AppliedAt          *metav1.Time        `json:"appliedAt,omitempty"`
    ScheduledRestartAt *metav1.Time        `json:"scheduledRestartAt,omitempty"`
    RevertBefore       *metav1.Time        `json:"revertBefore,omitempty"`
    RevertConfig       *ResourceSpec       `json:"revertConfig,omitempty"`
    Message            string              `json:"message,omitempty"`
}
```

The `State` field follows a well-defined state machine:

```
                         +---------+
                         | pending |
                         +----+----+
                              |
                     (user or auto-approve)
                              |
                         +----v----+
                         | approved|
                         +----+----+
                              |
                    (controller patches VM)
                              |
              +---------------+---------------+
              |                               |
   +----------v-----------+          +--------v--------+
   | applied-pending-     |          |     applied     |
   | restart              |          |  (hotplug OK)   |
   +----------+-----------+          +--------+--------+
              |                               |
       (VM restarted)                         |
              |                               |
       +------v------+                        |
       |   applied   |                        |
       +------+------+                        |
              |                               |
              +-------------------------------+
              |                               |
       +------v------+                 +------v------+
       |  reverted   |                 |   failed    |
       +-------------+                 +-------------+
```

The six states are:

```go
const (
    StatePending               RecommendationState = "pending"
    StateApproved              RecommendationState = "approved"
    StateAppliedPendingRestart RecommendationState = "applied-pending-restart"
    StateApplied               RecommendationState = "applied"
    StateReverted              RecommendationState = "reverted"
    StateFailed                RecommendationState = "failed"
)
```

Walking through the lifecycle:

1. **pending** -- The recommendation has been created. It is waiting for someone (a human operator or the auto-mode controller) to approve it.
2. **approved** -- The recommendation has been approved. The controller will now attempt to patch the VM's resource spec.
3. **applied-pending-restart** -- The VM spec has been patched, but the VM needs a restart for the changes to take effect (this happens when hotplug is not available). The `ScheduledRestartAt` timestamp tracks when the restart is planned.
4. **applied** -- The new resource allocation is live. The `RevertConfig` preserves the original settings in case a rollback is needed. The `RevertBefore` timestamp defines the window during which a revert is possible.
5. **reverted** -- The recommendation was rolled back to the original configuration, either automatically (e.g., the revert retention window expired) or manually.
6. **failed** -- Something went wrong during application. The `Message` field carries the error details.

### Kubebuilder Markers

The type definition carries several kubebuilder markers that control code generation:

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Direction",type=string,JSONPath=`.spec.direction`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="VM",type=string,JSONPath=`.spec.virtualMachineRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RightsizingRecommendation struct { ... }
```

- `+kubebuilder:object:root=true` -- Marks this as a top-level API type (not a nested struct). The code generator will produce `DeepCopyObject()` and register it with the scheme.
- `+kubebuilder:subresource:status` -- Enables the `/status` subresource. This means the spec and status are updated through separate API endpoints, so a controller updating the status cannot accidentally modify the spec (and vice versa).
- `+kubebuilder:printcolumn` -- Defines the columns shown when you run `oc get rightsizingrecommendations`. The four columns give you Direction, State, VM name, and Age at a glance, without needing `-o yaml`.

## RightsizingPolicy

**Source file:** `api/v1alpha1/rightsizingpolicy_types.go`

This CRD is **cluster-scoped** -- there is one policy for the entire cluster, and it does not live in any namespace. It configures how OVRO calculates recommendations and what it does with them.

### Cluster Scope

The cluster scope is declared with a kubebuilder marker:

```go
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
type RightsizingPolicy struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`

    Spec RightsizingPolicySpec `json:"spec,omitempty"`
}
```

The `+kubebuilder:resource:scope=Cluster` marker tells the code generator and CRD YAML that this resource has no namespace. You create it with `oc apply -f policy.yaml` without a `metadata.namespace` field.

Note that the policy has no status subresource -- it is purely declarative configuration.

### AlgorithmSpec -- How to Calculate

```go
type AlgorithmSpec struct {
    Percentile      int `json:"percentile"`
    HeadroomPercent int `json:"headroomPercent"`
}
```

- **Percentile** -- Which percentile of observed utilization to use as the baseline. A value of 95 means "size the VM so it can handle 95% of observed workload without stress." Higher values are more conservative (less likely to cause performance issues), lower values are more aggressive (save more resources).
- **HeadroomPercent** -- Additional buffer added on top of the percentile-based recommendation. A headroom of 20 means "after calculating the P95-based size, add 20% more." This accounts for workload growth and unexpected spikes.

### ThresholdsSpec -- When to Act

```go
type ThresholdsSpec struct {
    MinCPUSavings            int32             `json:"minCpuSavings"`
    MinMemorySavings         resource.Quantity `json:"minMemorySavings"`
    UpsizeUtilizationPercent int               `json:"upsizeUtilizationPercent"`
}
```

- **MinCPUSavings** -- Minimum number of CPU cores that must be saved before a downsize recommendation is generated. Prevents noisy recommendations for trivial gains.
- **MinMemorySavings** -- Same concept for memory (e.g., `"1Gi"` means do not recommend downsizing unless at least 1 GiB can be freed).
- **UpsizeUtilizationPercent** -- The utilization percentage above which an upsize recommendation is triggered. At 90, a VM whose P95 CPU or memory usage exceeds 90% will get an upsize recommendation.

### AutoModeSpec -- Hands-Free Operation

```go
type AutoModeSpec struct {
    Enabled         bool   `json:"enabled"`
    Schedule        string `json:"schedule"`
    RequireApproval bool   `json:"requireApproval"`
}
```

- **Enabled** -- Whether auto-mode is active at all.
- **Schedule** -- A cron expression defining when auto-mode runs (e.g., apply changes during maintenance windows).
- **RequireApproval** -- When true, auto-mode still creates recommendations in `pending` state and waits for manual approval. When false, recommendations are auto-approved and applied without human intervention.

### RightsizingPolicySpec -- Putting It Together

```go
type RightsizingPolicySpec struct {
    LookbackDays             int            `json:"lookbackDays"`
    Algorithm                AlgorithmSpec  `json:"algorithm"`
    Thresholds               ThresholdsSpec `json:"thresholds"`
    RevertRetentionDays      int            `json:"revertRetentionDays"`
    AutoMode                 AutoModeSpec   `json:"autoMode"`
    ReconcileIntervalMinutes int            `json:"reconcileIntervalMinutes"`
}
```

- **LookbackDays** -- How many days of historical metrics to query from Prometheus. Longer windows smooth out anomalies but respond more slowly to workload changes.
- **RevertRetentionDays** -- How many days to keep the revert configuration after applying a recommendation. After this window expires, the old config is discarded and the recommendation cannot be rolled back.
- **ReconcileIntervalMinutes** -- How often the controller re-evaluates all VMs. This is the polling interval for the main reconciliation loop.

## Shared Defaults

**Source file:** `api/v1alpha1/defaults.go`

The `DefaultPolicySpec()` function provides sensible defaults for every policy field:

```go
const AnnotationExclude = "rightsizing.redhatconsulting.io/exclude"

func DefaultPolicySpec() RightsizingPolicySpec {
    return RightsizingPolicySpec{
        LookbackDays: 14,
        Algorithm: AlgorithmSpec{
            Percentile:      95,
            HeadroomPercent: 20,
        },
        Thresholds: ThresholdsSpec{
            MinCPUSavings:            1,
            MinMemorySavings:         resource.MustParse("1Gi"),
            UpsizeUtilizationPercent: 90,
        },
        RevertRetentionDays:      30,
        ReconcileIntervalMinutes: 60,
    }
}
```

These defaults mean that, out of the box, OVRO will:

| Parameter | Default | Meaning |
|---|---|---|
| LookbackDays | 14 | Use 2 weeks of metrics history |
| Percentile | 95 | Size for the 95th percentile workload |
| HeadroomPercent | 20 | Add 20% safety margin |
| MinCPUSavings | 1 | Only recommend if at least 1 CPU core is saved |
| MinMemorySavings | 1Gi | Only recommend if at least 1 GiB memory is saved |
| UpsizeUtilizationPercent | 90 | Recommend upsize when utilization exceeds 90% |
| RevertRetentionDays | 30 | Keep rollback option for 30 days |
| ReconcileIntervalMinutes | 60 | Re-evaluate all VMs every hour |

If no `RightsizingPolicy` CR exists on the cluster, the controller calls `DefaultPolicySpec()` and uses these values. Operators can override any subset by creating a policy CR with only the fields they want to change.

### The Exclude Annotation

```go
const AnnotationExclude = "rightsizing.redhatconsulting.io/exclude"
```

Any VirtualMachine annotated with this key is completely skipped by the recommendation engine. This is the escape hatch for VMs that should never be rightsized -- perhaps they have fixed licensing requirements, are used for benchmarking, or have workloads too irregular for statistical analysis.

To exclude a VM:

```bash
oc annotate vm my-special-vm rightsizing.redhatconsulting.io/exclude=true
```

## Key Takeaways

1. **Two CRDs, two roles.** `RightsizingRecommendation` is the per-VM prescription (namespaced, carries data and status). `RightsizingPolicy` is the cluster-wide configuration (cluster-scoped, no status, pure configuration).

2. **The state machine is the workflow engine.** The six states (pending, approved, applied-pending-restart, applied, reverted, failed) define every valid transition in the recommendation lifecycle. Controllers move recommendations through these states; the UI and CLI read them.

3. **Metrics are baked into the recommendation.** The `MetricsSnapshot` field means every recommendation is self-documenting -- you can always see the data that justified it, even months later.

4. **Defaults keep it simple.** `DefaultPolicySpec()` provides production-ready defaults so the operator works without any configuration. Cluster admins only need to create a `RightsizingPolicy` CR if they want to override something.

5. **Exclude annotation is the escape hatch.** The `AnnotationExclude` constant provides a clean, per-VM opt-out mechanism.

6. **Kubebuilder markers drive code generation.** The `+kubebuilder:object:root`, `+kubebuilder:subresource:status`, and `+kubebuilder:resource:scope=Cluster` markers control how the CRD manifests and Go helpers are generated -- no manual YAML editing needed.

## Next Chapter

In [Chapter 2: Prometheus Integration](02-Prometheus-Integration.md), we will see how OVRO queries Prometheus for the raw utilization metrics that feed into these CRD types. You will learn about the PromQL queries, the metrics client interface, and how lookback windows and percentile calculations turn time-series data into the `MetricsSnapshot` that appears in every recommendation.
