---
title: "Chapter 4: Recommendation Controller"
order: 4
---

# Chapter 4: Recommendation Controller

In the previous chapters we built the foundation pieces of OVRO one at a time: the [CRD types](01-CRD-Types.md) that define what a recommendation *looks like* in the Kubernetes API, the [Prometheus client](02-Prometheus-Client.md) that knows how to *measure* a VM's resource usage, and the [calculator](03-Calculator.md) that turns those measurements into a *resize verdict*. Each piece works independently and is individually testable -- but none of them *does* anything on its own.

The **Recommendation Controller** is where all three pieces come together. It is the orchestrator that continuously watches every VirtualMachine in the cluster, pulls the right data from the right places, and produces actionable `RightsizingRecommendation` Custom Resources that the rest of the system (and human operators) can act on.

---

## The Doctor Analogy

Think of the controller as a doctor running a clinic with a rotating schedule of patient checkups:

1. **Check the chart** -- look up the treatment guidelines for this patient (fetch the `RightsizingPolicy`, or fall back to sensible defaults).
2. **Examine the patient** -- read the VM's current CPU and memory allocation directly from its Kubernetes spec.
3. **Run lab tests** -- query Prometheus for real-world utilisation metrics over the configured lookback window.
4. **Consult the diagnostic manual** -- feed the examination results and lab work into the rightsizing calculator.
5. **Write a prescription (or not)** -- if the calculator says the VM needs resizing, create or update a `RightsizingRecommendation` CR. If the VM is healthy, do nothing.
6. **Schedule the next appointment** -- requeue the reconciliation so the VM is checked again at the configured interval.

This pattern repeats for every VM in every namespace, running continuously as long as the operator is alive.

---

## Key Types and Interfaces

Before diving into the reconciliation flow, let's look at the types the controller defines and depends on.

### The Reconciler Struct

```go
type RightsizingRecommendationReconciler struct {
    client.Client
    Scheme     *runtime.Scheme
    PromClient PrometheusQuerier
    Log        logr.Logger
}
```

This struct follows the standard controller-runtime pattern:

- **`client.Client`** (embedded) -- provides `Get`, `List`, `Create`, `Update`, and `Status().Update()` methods for reading and writing Kubernetes resources. Embedding it means the reconciler *is* a client, so you can call `r.Get(...)` directly.
- **`Scheme`** -- the runtime scheme that maps Go types to Kubernetes GroupVersionKinds. Needed by the client to serialise and deserialise resources correctly.
- **`PromClient`** -- a `PrometheusQuerier` interface (see below). This is the controller's only connection to Prometheus, and it is injected at startup. Swapping it for a mock in tests is trivial.
- **`Log`** -- a structured logger from the `logr` package, used throughout reconciliation to produce human-readable, filterable log lines.

### The PrometheusQuerier Interface

```go
type PrometheusQuerier interface {
    GetVMUtilization(ctx context.Context, vmName, namespace string, lookbackDays int) (*VMUtilization, error)
}
```

This single-method interface is the boundary between the controller and the metrics layer. The real implementation (covered in [Chapter 2](02-Prometheus-Client.md)) fires PromQL queries against a live Prometheus or Thanos instance. But because the controller only knows about this interface -- never the concrete struct -- you can substitute a fake implementation in unit tests that returns canned data. This is a textbook application of **dependency injection via interfaces** in Go.

### The VMUtilization Struct

```go
type VMUtilization struct {
    CurrentCPUCores  int32
    CurrentMemoryGiB int32
    CPUP95Percent    float64
    MemoryP95Percent float64
    CPUMaxPercent    float64
    MemoryMaxPercent float64
}
```

This is the data contract between the Prometheus layer and the controller. The `CurrentCPUCores` and `CurrentMemoryGiB` fields are populated by the controller itself (from the VM spec), while the percentage fields come from Prometheus. Together, they contain everything the calculator needs to produce a recommendation.

---

## Reading VM Resources with Unstructured Objects

Before querying Prometheus, the controller needs to know what the VM is *currently* allocated. That information lives in the `VirtualMachine` custom resource managed by KubeVirt. Here's the challenge: OVRO does not import the KubeVirt Go types as a dependency. KubeVirt's type packages are large, bring in transitive dependencies, and would tightly couple OVRO's build to a specific KubeVirt version.

Instead, the controller uses Kubernetes **unstructured objects** -- a generic map-based representation that can hold any resource without compile-time type information:

```go
func (r *RightsizingRecommendationReconciler) getVMResources(ctx context.Context, name, namespace string) (cpuCores int32, memoryGiB int32, err error) {
    vm := &unstructured.Unstructured{}
    vm.SetGroupVersionKind(schema.GroupVersionKind{
        Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine",
    })
    if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, vm); err != nil {
        return 0, 0, fmt.Errorf("fetching VM: %w", err)
    }

    cores, found, _ := unstructured.NestedInt64(vm.Object,
        "spec", "template", "spec", "domain", "cpu", "cores")
    if found {
        cpuCores = int32(cores)
    }

    memStr, found, _ := unstructured.NestedString(vm.Object,
        "spec", "template", "spec", "domain", "resources", "requests", "memory")
    if found {
        q, parseErr := resource.ParseQuantity(memStr)
        if parseErr == nil {
            memoryGiB = int32(q.Value() / (1024 * 1024 * 1024))
        }
    }

    return cpuCores, memoryGiB, nil
}
```

Walking through it:

1. **Create an empty unstructured object** and set its GVK to `kubevirt.io/v1/VirtualMachine`. This tells the Kubernetes client *what kind of resource* to fetch, without needing compiled Go types.
2. **Fetch the VM** from the API server using the standard `client.Client.Get()` method. The result arrives as a nested `map[string]interface{}`.
3. **Extract CPU cores** by navigating the nested path `spec.template.spec.domain.cpu.cores` using the `unstructured.NestedInt64` helper. This mirrors the YAML structure of a KubeVirt VirtualMachine.
4. **Extract memory** from `spec.template.spec.domain.resources.requests.memory`. Since memory is expressed as a Kubernetes quantity string (e.g., `"8Gi"`), it is parsed with `resource.ParseQuantity` and converted to whole GiB.

This approach trades compile-time safety for decoupling. If KubeVirt changes the location of these fields in a future version, the code will silently return zero rather than failing to compile -- but the tradeoff is worth it for an operator that needs to work across multiple KubeVirt versions without vendoring their types.

---

## The Reconcile Method

The `Reconcile` method is the heart of the controller. It is called by controller-runtime every time a VirtualMachine changes or when a requeue timer fires. Here is the full method:

```go
func (r *RightsizingRecommendationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := r.Log.WithValues("vm", req.NamespacedName)

    // 1. Fetch the cluster-wide RightsizingPolicy (or use defaults).
    policy := &rightsizingv1alpha1.RightsizingPolicy{}
    if err := r.Get(ctx, types.NamespacedName{Name: "default"}, policy); err != nil {
        if errors.IsNotFound(err) {
            log.Info("No RightsizingPolicy found, using defaults")
            policy = defaultPolicy()
        } else {
            return ctrl.Result{}, fmt.Errorf("fetching policy: %w", err)
        }
    }

    requeueAfter := time.Duration(policy.Spec.ReconcileIntervalMinutes) * time.Minute

    // 2. Fetch VM current resources from the VirtualMachine object.
    cpuCores, memoryGiB, err := r.getVMResources(ctx, req.Name, req.Namespace)
    if err != nil {
        log.Error(err, "Failed to fetch VM resources")
        return ctrl.Result{RequeueAfter: requeueAfter}, nil
    }

    // 3. Query Prometheus for VM utilization metrics.
    utilization, err := r.PromClient.GetVMUtilization(ctx, req.Name, req.Namespace, policy.Spec.LookbackDays)
    if err != nil {
        log.Error(err, "Failed to query Prometheus")
        return ctrl.Result{RequeueAfter: requeueAfter}, nil
    }
    utilization.CurrentCPUCores = cpuCores
    utilization.CurrentMemoryGiB = memoryGiB

    // 4. Run the rightsizing calculator.
    input := calculator.AnalysisInput{
        CurrentCPUCores:     utilization.CurrentCPUCores,
        CurrentMemoryGiB:    utilization.CurrentMemoryGiB,
        CPUP95Percent:       utilization.CPUP95Percent,
        MemoryP95Percent:    utilization.MemoryP95Percent,
        CPUMaxPercent:       utilization.CPUMaxPercent,
        MemoryMaxPercent:    utilization.MemoryMaxPercent,
        HeadroomPercent:     policy.Spec.Algorithm.HeadroomPercent,
        MinCPUSavings:       policy.Spec.Thresholds.MinCPUSavings,
        MinMemorySavingsGiB: int32(policy.Spec.Thresholds.MinMemorySavings.Value() / (1024 * 1024 * 1024)),
        UpsizeThresholdPct:  policy.Spec.Thresholds.UpsizeUtilizationPercent,
    }

    result := calculator.Analyze(input)
    if result == nil {
        log.Info("No recommendation needed for VM")
        return ctrl.Result{RequeueAfter: requeueAfter}, nil
    }

    // 5. Create or update the RightsizingRecommendation resource.
    now := metav1.Now()
    rec := &rightsizingv1alpha1.RightsizingRecommendation{}
    recName := types.NamespacedName{Name: "vm-" + req.Name, Namespace: req.Namespace}
    err = r.Get(ctx, recName, rec)

    if errors.IsNotFound(err) {
        // Create a new recommendation.
        rec = &rightsizingv1alpha1.RightsizingRecommendation{
            ObjectMeta: metav1.ObjectMeta{
                Name:      recName.Name,
                Namespace: recName.Namespace,
            },
            Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
                VirtualMachineRef: rightsizingv1alpha1.ObjectRef{
                    Name: req.Name, Namespace: req.Namespace,
                },
                Direction:   rightsizingv1alpha1.RecommendationDirection(result.Direction),
                Current:     /* ... current resources ... */,
                Recommended: /* ... recommended resources ... */,
                Savings:     /* ... savings ... */,
                Metrics:     /* ... metrics snapshot ... */,
            },
        }

        if err := r.Create(ctx, rec); err != nil {
            return ctrl.Result{}, fmt.Errorf("creating recommendation: %w", err)
        }

        rec.Status.State = rightsizingv1alpha1.StatePending
        rec.Status.LastCalculated = &now
        if err := r.Status().Update(ctx, rec); err != nil {
            return ctrl.Result{}, fmt.Errorf("updating recommendation status: %w", err)
        }
    } else if err != nil {
        return ctrl.Result{}, fmt.Errorf("fetching recommendation: %w", err)
    } else {
        // Skip update if already applied.
        if rec.Status.State == rightsizingv1alpha1.StateApplied ||
            rec.Status.State == rightsizingv1alpha1.StateAppliedPendingRestart {
            return ctrl.Result{RequeueAfter: requeueAfter}, nil
        }

        // Update the existing recommendation with fresh calculations.
        rec.Spec.Direction = rightsizingv1alpha1.RecommendationDirection(result.Direction)
        rec.Spec.Recommended = /* ... updated recommended resources ... */
        rec.Spec.Savings = /* ... updated savings ... */

        if err := r.Update(ctx, rec); err != nil {
            return ctrl.Result{}, fmt.Errorf("updating recommendation: %w", err)
        }

        rec.Status.LastCalculated = &now
        if err := r.Status().Update(ctx, rec); err != nil {
            return ctrl.Result{}, fmt.Errorf("updating recommendation status: %w", err)
        }
    }

    return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
```

> The struct field assignments for `Current`, `Recommended`, `Savings`, and `Metrics` are abbreviated above for readability. The full code populates every field from the calculator result and utilisation data -- see the source file for the complete version.

Let's trace through the numbered steps:

### Step 1: Fetch the Policy

The controller tries to load a cluster-scoped `RightsizingPolicy` named `"default"`. If none exists, it falls back to `defaultPolicy()`, which calls `DefaultPolicySpec()` from the API types package (covered in [Chapter 1](01-CRD-Types.md)). The defaults provide a 14-day lookback, 20% headroom, and a 60-minute reconcile interval -- reasonable starting values that work without any configuration.

### Step 2: Read Current VM Resources

The controller calls `getVMResources()` to fetch the VM's current CPU cores and memory from its KubeVirt spec. If the VM cannot be found (perhaps it was deleted between the event and the reconciliation), the error is logged and the reconciliation is requeued -- it does not return a hard error, because the VM might reappear or this might be a transient issue.

### Step 3: Query Prometheus

Using the injected `PromClient`, the controller calls `GetVMUtilization()` with the VM's name, namespace, and the policy's lookback period. The Prometheus client (see [Chapter 2](02-Prometheus-Client.md)) fires PromQL queries for P95 and max CPU/memory utilisation and returns the results in a `VMUtilization` struct. The controller then stamps the current allocation (`cpuCores`, `memoryGiB`) onto the struct, because those values come from the VM spec, not from Prometheus.

### Step 4: Run the Calculator

The controller populates a `calculator.AnalysisInput` struct with everything the calculator needs: current allocations, utilisation percentages, and policy thresholds. It then calls `calculator.Analyze()` (see [Chapter 3](03-Calculator.md)). If `Analyze` returns `nil`, the VM is appropriately sized -- the controller logs this and requeues without creating a recommendation.

### Step 5: Create or Update the Recommendation

This is where the controller writes its "prescription." It checks whether a `RightsizingRecommendation` already exists for this VM (using the naming convention `vm-<vmName>`):

- **If not found** -- a new recommendation is created with the full spec (current resources, recommended resources, savings, metrics snapshot, and direction). After creation, the status subresource is updated separately to set the state to `Pending` and record the `LastCalculated` timestamp.
- **If found but already applied** -- the controller skips the update. Once a recommendation has been applied (state is `Applied` or `AppliedPendingRestart`), it should not be overwritten until the next cycle naturally produces a new assessment after the resize takes effect.
- **If found and still pending or approved** -- the existing recommendation is updated with fresh calculation results and a new `LastCalculated` timestamp.

Notice that the spec and status updates are separate API calls. This is required by Kubernetes: the main resource and its status subresource are updated through different endpoints, and attempting to update both in a single call will silently drop the status changes.

### Step 6: Requeue

Every return path includes `RequeueAfter: requeueAfter`, where `requeueAfter` is derived from the policy's `ReconcileIntervalMinutes` (default: 60 minutes). This ensures the controller periodically re-evaluates every VM even if no Kubernetes events occur. The controller-runtime framework handles the timer internally.

---

## The Controller-Runtime Reconciliation Loop

It is worth stepping back to understand the framework pattern that makes all of this work. The `Reconcile` method does not run in a loop that the developer writes. Instead, controller-runtime provides a **work queue** that receives events (create, update, delete) for watched resources. Each event is mapped to a `ctrl.Request` (just a `NamespacedName`), deduplicated, and dispatched to your `Reconcile` method.

This means:

- **Reconcile is called with a name, not an object.** Your first job is always to fetch the current state from the API server. If the resource is gone, you handle cleanup.
- **Reconcile must be idempotent.** It might be called multiple times for the same resource with no actual change. It might be called out of order. Your logic must converge to the correct state regardless.
- **Requeue is the only way to schedule future work.** If you need to re-evaluate a resource later, you return `ctrl.Result{RequeueAfter: duration}`. The framework handles the rest.

This is fundamentally different from a polling loop. Events arrive immediately when resources change, and scheduled requeues fill the gap for time-based reevaluation.

---

## Wiring It Up: SetupWithManager

The `SetupWithManager` method tells controller-runtime which resources to watch and how to map events to reconciliation requests:

```go
func (r *RightsizingRecommendationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    vmGVK := schema.GroupVersionKind{
        Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine",
    }
    vm := &unstructured.Unstructured{}
    vm.SetGroupVersionKind(vmGVK)

    return ctrl.NewControllerManagedBy(mgr).
        Named("rightsizingrecommendation").
        WatchesRawSource(source.Kind(
            mgr.GetCache(), vm,
            &handler.TypedEnqueueRequestForObject[*unstructured.Unstructured]{},
        )).
        Complete(r)
}
```

There are three important design decisions here:

### Why `WatchesRawSource` instead of `For`?

The typical controller-runtime pattern uses `.For(&MyType{})` to watch a typed resource. But `VirtualMachine` is not a type that OVRO defines -- it belongs to KubeVirt, and OVRO deliberately avoids importing KubeVirt's Go types. The `WatchesRawSource` method accepts an `unstructured.Unstructured` object with a manually-set GVK, allowing the controller to watch resources it does not have compiled types for.

### Why `source.Kind`?

`source.Kind` creates an event source from the manager's shared informer cache. This means the controller uses the same efficient watch-and-cache mechanism as any typed controller -- it does not re-list VMs on every reconciliation. The cache is populated by a single watch connection to the API server, and events are delivered to the controller's work queue as they arrive.

### Why `TypedEnqueueRequestForObject`?

This event handler maps each VirtualMachine event directly to a reconciliation request with the VM's name and namespace. When a VM is created, updated, or deleted, the handler enqueues a `ctrl.Request{NamespacedName: ...}` that lands in the `Reconcile` method's `req` parameter. This is the standard mapping for a controller that watches the resources it operates on.

---

## The Default Policy Helper

```go
func defaultPolicy() *rightsizingv1alpha1.RightsizingPolicy {
    return &rightsizingv1alpha1.RightsizingPolicy{
        Spec: rightsizingv1alpha1.DefaultPolicySpec(),
    }
}
```

This is a thin wrapper that constructs a `RightsizingPolicy` with the default spec values defined in the API types package. It exists so the reconciler can treat the "no policy configured" case identically to the "policy exists" case -- the rest of the method just reads `policy.Spec.*` without caring where the values came from.

The defaults (from `DefaultPolicySpec()`) are:

| Setting | Default |
|---|---|
| Lookback days | 14 |
| Percentile | 95 |
| Headroom | 20% |
| Min CPU savings | 1 core |
| Min memory savings | 1 GiB |
| Upsize threshold | 90% utilisation |
| Reconcile interval | 60 minutes |
| Revert retention | 30 days |

These were chosen to be conservative -- the operator will not recommend a resize unless the savings are meaningful and the data backing the recommendation spans at least two weeks.

---

## Interface-Based Testing

One of the most important architectural decisions in this controller is the `PrometheusQuerier` interface. In production, the controller receives a real Prometheus client that fires HTTP queries against a live metrics store. In tests, it receives a mock:

```go
type fakePrometheusClient struct {
    utilization *VMUtilization
    err         error
}

func (f *fakePrometheusClient) GetVMUtilization(ctx context.Context, vmName, namespace string, lookbackDays int) (*VMUtilization, error) {
    return f.utilization, f.err
}
```

This lets you test the entire reconciliation flow -- policy lookup, calculator invocation, recommendation creation, status updates -- without ever touching a real Prometheus instance. You control exactly what metrics the controller sees, which makes it possible to write deterministic, fast tests for edge cases like "Prometheus returns an error" or "utilisation is exactly at the threshold."

This pattern applies broadly in Go: **define interfaces at the consumer, not the provider**. The controller defines `PrometheusQuerier` because the controller is the one that needs the abstraction. The Prometheus client package does not need to know about this interface at all.

---

## Sequence of Operations

Here is the complete flow visualised as a sequence:

```
VirtualMachine event arrives
        |
        v
[Reconcile called with VM name + namespace]
        |
        v
[Fetch RightsizingPolicy "default"]
  found? --> use it
  not found? --> use defaultPolicy()
  other error? --> return error (retry)
        |
        v
[getVMResources: fetch VM spec via unstructured client]
  error? --> log, requeue after interval
        |
        v
[PromClient.GetVMUtilization: query Prometheus]
  error? --> log, requeue after interval
        |
        v
[Populate calculator.AnalysisInput]
        |
        v
[calculator.Analyze(input)]
  nil? --> "no recommendation needed", requeue
        |
        v
[Check for existing RightsizingRecommendation "vm-<name>"]
        |
   +----+----+
   |         |
 not found  found
   |         |
   v         v
 Create    State == Applied or AppliedPendingRestart?
   |         |           |
   v        yes          no
 Set        |            |
 status     v            v
 Pending  skip        Update spec + savings
   |       requeue    Update LastCalculated
   v         |            |
   +----+----+----+-------+
        |
        v
  [return RequeueAfter: interval]
```

---

## Key Takeaways

- **The Recommendation Controller is the orchestrator** that ties together CRD types, Prometheus queries, and the rightsizing calculator into a continuous reconciliation loop.
- **Unstructured objects** let the controller interact with KubeVirt `VirtualMachine` resources without importing KubeVirt's Go type packages, keeping the dependency tree lean and version-flexible.
- **`WatchesRawSource`** with `source.Kind` enables watching resources that have no compiled Go types, using the same efficient informer-cache mechanism as typed watches.
- **The controller-runtime reconciliation loop** is event-driven with timer-based requeues -- not a polling loop. `Reconcile` is called on resource changes and at scheduled intervals.
- **Interface-based dependency injection** (the `PrometheusQuerier` interface) makes the controller fully testable without a live Prometheus instance.
- **Status subresource updates** are separate from spec updates -- this is a Kubernetes requirement, not a design choice.
- **The "skip if applied" guard** prevents the controller from overwriting recommendations that are already being acted upon, avoiding conflicts with the applier and restart controller.
- **Sensible defaults** via `DefaultPolicySpec()` mean the operator works out of the box with no `RightsizingPolicy` configured.

---

## What's Next?

The Recommendation Controller produces recommendations, but it does not *act* on them. In [Chapter 5: VM Applier and Restart Controller](05-VM-Applier-and-Restart-Controller.md), we will see how approved recommendations are applied to VirtualMachine specs and how the restart controller coordinates VM restarts to make the changes take effect -- completing the loop from observation to action.
