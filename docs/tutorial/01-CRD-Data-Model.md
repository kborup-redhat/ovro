---
title: "Chapter 1: CRD Data Model"
order: 1
---

# Chapter 1: CRD Data Model

## Introduction

Every Kubernetes operator starts with its data model. OVRO defines two Custom Resource Definitions (CRDs) that act as the shared language between all components: **RightsizingRecommendation** holds individual VM analysis results, and **RightsizingPolicy** controls operator behaviour cluster-wide. Think of them as database tables, except they live in the Kubernetes API and get the full benefit of watches, RBAC, and the controller reconciliation pattern.

## RightsizingRecommendation

This is the core resource. One exists per VM that has an actionable recommendation. It captures what the VM currently has, what OVRO recommends, the utilisation metrics behind the recommendation, and a state machine tracking the lifecycle from creation through approval to application.

### Spec: The Desired State

```go
// api/v1alpha1/rightsizingrecommendation_types.go

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

Key fields:

- **VirtualMachineRef** — links the recommendation to a specific VM by name and namespace.
- **Direction** — either `"downsize"` (VM is over-provisioned) or `"upsize"` (VM is under-provisioned).
- **Current / Recommended** — CPU cores, sockets, threads, and memory quantities. The `ResourceSpec` mirrors the KubeVirt VM domain structure.
- **Savings** — the delta between current and recommended, making it easy to aggregate cluster-wide savings.
- **Metrics** — a snapshot of the utilisation percentiles (P95, max) and lookback window used for the calculation. This lets reviewers understand *why* the recommendation was made.
- **HotplugCapable** — whether the VM supports live CPU/memory changes without a restart.

### Status: The Current State

```go
type RightsizingRecommendationStatus struct {
    State              RecommendationState `json:"state,omitempty"`
    LastCalculated     *metav1.Time        `json:"lastCalculated,omitempty"`
    AppliedAt          *metav1.Time        `json:"appliedAt,omitempty"`
    ScheduledRestartAt *metav1.Time        `json:"scheduledRestartAt,omitempty"`
    RevertBefore       *metav1.Time        `json:"revertBefore,omitempty"`
    RevertConfig       *ResourceSpec       `json:"revertConfig,omitempty"`
    Message            string              `json:"message,omitempty"`
    Owner              string              `json:"owner,omitempty"`
    ApprovalToken      string              `json:"approvalToken,omitempty"`
    NotifiedAt         *metav1.Time        `json:"notifiedAt,omitempty"`
    ReminderSentAt     *metav1.Time        `json:"reminderSentAt,omitempty"`
    // ... approval and rejection tracking fields
}
```

The **State** field drives a state machine:

```
pending -> awaiting-approval -> approved -> applied-pending-restart -> applied
                              |                                        |
                              v                                        v
                           (rejected -> pending)                    reverted
```

- `pending` — recommendation created, waiting for admin action.
- `awaiting-approval` — admin clicked "Rightsize", owner has been notified.
- `applied-pending-restart` — VM spec updated, but a restart is needed.
- `applied` — changes are live.
- `reverted` — changes were rolled back.
- `failed` — something went wrong.

The **RevertConfig** stores the original VM resources so changes can be undone. Approval fields (`Owner`, `ApprovalToken`, `NotifiedAt`, `ReminderSentAt`) track the approval workflow lifecycle.

### Supporting Types

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

These mirror KubeVirt's CPU topology and use Kubernetes `resource.Quantity` for memory (e.g., `"8Gi"`), ensuring compatibility when patching VM specs.

## RightsizingPolicy

A single cluster-scoped resource named `default` controls operator behaviour:

```go
// api/v1alpha1/rightsizingpolicy_types.go

type RightsizingPolicySpec struct {
    LookbackDays             int            `json:"lookbackDays"`
    Algorithm                AlgorithmSpec  `json:"algorithm"`
    Thresholds               ThresholdsSpec `json:"thresholds"`
    RevertRetentionDays      int            `json:"revertRetentionDays"`
    AutoMode                 AutoModeSpec   `json:"autoMode"`
    ReconcileIntervalMinutes int            `json:"reconcileIntervalMinutes"`
}
```

- **LookbackDays** — how many days of metric history to analyse (default: 14).
- **Algorithm.Percentile** — which percentile to use (default: P95). P99 is more conservative; P50 is more aggressive.
- **Algorithm.HeadroomPercent** — extra capacity above the percentile (default: 20%). A 20% headroom means "if P95 CPU is 4 cores, recommend 4.8, rounded up to 5."
- **Thresholds** — minimum savings required to generate a recommendation, preventing trivial changes.
- **ReconcileIntervalMinutes** — how often each VM is re-evaluated (default: 60 minutes).

### Defaults

```go
// api/v1alpha1/defaults.go

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

If no `RightsizingPolicy` CR exists, the controller falls back to these defaults. This means OVRO works out-of-the-box without any configuration.

### Constants and Labels

```go
const AnnotationExclude = "rightsizing.redhatconsulting.io/exclude"
const LabelOwner = "rightsizing.redhatconsulting.io/owner"
```

- **AnnotationExclude** — set on a VM to opt it out of analysis entirely.
- **LabelOwner** — set on a VM or namespace to designate the owner for the approval workflow. VM-level labels take precedence over namespace-level labels.

## Key Takeaways

- OVRO uses two CRDs: `RightsizingRecommendation` (per-VM analysis) and `RightsizingPolicy` (cluster-wide config).
- The recommendation status tracks a multi-state lifecycle from pending through approval to application and optional revert.
- Types mirror KubeVirt's CPU topology and use `resource.Quantity` for memory, ensuring seamless patching.
- Sensible defaults mean the operator works without any configuration.
- Owner labels and exclude annotations are the primary control surfaces for VM-level behaviour.

## Next Steps

Now that we understand the data model, let's look at how OVRO collects the metrics that drive its recommendations.
