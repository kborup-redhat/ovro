---
title: "Chapter 5: VM Applier and Restart Controller"
order: 5
---

# Chapter 5: VM Applier and Restart Controller

In the previous chapters we built a recommendation engine that watches VirtualMachines, queries Prometheus for utilisation metrics, and writes `RightsizingRecommendation` CRs with safe resize targets. Those recommendations are useful, but they do not change anything by themselves -- they are prescriptions waiting to be filled.

This chapter covers the two components that turn recommendations into action:

1. **The VM Applier** -- patches a VirtualMachine's CPU and memory to match a recommendation.
2. **The Restart Controller** -- watches for recommendations in the `applied-pending-restart` state and triggers a VM restart at the scheduled time so the new resources take effect.

Think of it this way: the Applier is like a **pharmacist filling a prescription**. It takes the doctor's recommendation and actually changes the VM. The Restart Controller is like a **scheduler** that ensures the medicine is administered at exactly the right time -- not too early, not too late.

---

## The VM Applier

The Applier lives in `internal/applier/applier.go`. Its job is simple but critical: translate high-level resize instructions into low-level Kubernetes API calls against KubeVirt `VirtualMachine` resources.

### Why the Dynamic Client?

The first thing you will notice is that the Applier uses `k8s.io/client-go/dynamic.Interface` rather than a typed client generated from KubeVirt Go types. There is a deliberate reason for this.

KubeVirt's Go module (`kubevirt.io/api`) pulls in a large dependency tree and can create version conflicts with `controller-runtime` and other libraries in the operator's `go.mod`. By using the dynamic client, OVRO avoids importing KubeVirt types entirely. The trade-off is that we work with `unstructured.Unstructured` objects and raw `map[string]interface{}` structures instead of typed structs, but for the narrow set of fields we need to read and write, this is a worthwhile simplification.

The dynamic client needs a `GroupVersionResource` to know which API endpoint to call. This is defined once as a package-level variable:

```go
var vmGVR = schema.GroupVersionResource{
    Group:    "kubevirt.io",
    Version:  "v1",
    Resource: "virtualmachines",
}
```

Every method on the Applier uses this `vmGVR` constant to target the correct Kubernetes API path: `/apis/kubevirt.io/v1/namespaces/<ns>/virtualmachines/<name>`.

### The Applier Struct

The struct itself is minimal -- a thin wrapper around the dynamic client:

```go
type Applier struct {
    dynamicClient dynamic.Interface
}

func New(dynamicClient dynamic.Interface) *Applier {
    return &Applier{dynamicClient: dynamicClient}
}
```

This design keeps the Applier easy to test. In unit tests you can pass a `fake.FakeDynamicClient` and verify exactly which patches were sent without a real cluster.

### Fetching a VM with GetVM

Before patching anything, we often need to inspect the current state of a VM -- for example, to check whether it supports CPU hotplug. `GetVM` is a straightforward wrapper around the dynamic client's `Get` method:

```go
func (a *Applier) GetVM(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error) {
    return a.dynamicClient.Resource(vmGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}
```

The returned `*unstructured.Unstructured` gives us access to the entire VM spec as a nested `map[string]interface{}`, which we can traverse using helper functions from the `unstructured` package.

### Detecting Hotplug Capability

KubeVirt supports CPU hotplug -- adding CPU cores to a running VM without restarting it -- but only when the VM is configured with a `maxSockets` value greater than zero. This field tells KubeVirt the upper bound of CPU sockets the VM is allowed to use, and its presence signals that the VM firmware and guest OS support hot-adding CPUs.

```go
func IsHotplugCapable(vm *unstructured.Unstructured) bool {
    maxSockets, found, err := unstructured.NestedInt64(
        vm.Object, "spec", "template", "spec", "domain", "cpu", "maxSockets",
    )
    if err != nil || !found {
        return false
    }
    return maxSockets > 0
}
```

This function is a standalone utility (not a method on Applier) because it operates on an already-fetched VM object. The recommendation controller calls it during its reconcile loop to set the `HotplugCapable` field on each `RightsizingRecommendation`, which in turn determines whether a restart is needed after applying a resize.

The chain of nested keys -- `spec.template.spec.domain.cpu.maxSockets` -- mirrors the KubeVirt `VirtualMachine` spec structure. Working with `unstructured.NestedInt64` is slightly more verbose than accessing a typed struct field, but it avoids the dependency cost discussed above.

### Patching VM Resources

This is the core method. When a recommendation is approved and applied, `PatchVMResources` writes the new CPU core count and guest memory to the VM spec:

```go
func (a *Applier) PatchVMResources(ctx context.Context, name, namespace string, cpuCores int32, memory resource.Quantity) error {
    patch := map[string]interface{}{
        "spec": map[string]interface{}{
            "template": map[string]interface{}{
                "spec": map[string]interface{}{
                    "domain": map[string]interface{}{
                        "cpu": map[string]interface{}{"cores": cpuCores},
                        "memory": map[string]interface{}{"guest": memory.String()},
                    },
                },
            },
        },
    }

    patchBytes, err := json.Marshal(patch)
    if err != nil {
        return fmt.Errorf("marshaling patch: %w", err)
    }

    _, err = a.dynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
        ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
    )
    if err != nil {
        return fmt.Errorf("patching VM %s/%s: %w", namespace, name, err)
    }

    return nil
}
```

There are a few important details here.

**JSON Merge Patch vs Strategic Merge Patch.** The method uses `types.MergePatchType` (RFC 7396 JSON Merge Patch). With a merge patch, you provide a partial JSON document and the server merges it into the existing object: keys you include are updated, keys you omit are left unchanged. This is exactly what we want -- we are changing `cpu.cores` and `memory.guest` without touching any other part of the VM spec.

The alternative, `types.StrategicMergePatchType`, is the default for built-in Kubernetes resources and understands list semantics (e.g., merging container lists by name). However, strategic merge patches rely on `patchStrategy` struct tags in the Go type definitions, which are only available for types registered in the Kubernetes API machinery. Since KubeVirt's `VirtualMachine` is a CRD, not a built-in resource, strategic merge patches fall back to plain JSON merge behaviour anyway. Using `MergePatchType` explicitly makes the intent clear.

**The nested map structure.** The deeply nested `map[string]interface{}` mirrors the path to the fields we are changing: `spec.template.spec.domain.cpu.cores` and `spec.template.spec.domain.memory.guest`. Each level of nesting corresponds to a level in the KubeVirt VM spec. This is the cost of using the dynamic client -- you build the patch document by hand rather than populating typed struct fields.

**Memory as a string.** Notice that `memory` is serialized with `.String()` rather than being passed as a raw number. Kubernetes resource quantities like `"2Gi"` or `"512Mi"` are string-encoded in JSON. The `resource.Quantity` type's `String()` method produces the correct format.

### Restarting a VM

When a VM does not support hotplug (or when a memory change always requires a restart), the Applier can trigger a restart through KubeVirt's restart subresource:

```go
func (a *Applier) RestartVM(ctx context.Context, name, namespace string) error {
    body := []byte(`{}`)
    _, err := a.dynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
        ctx, name, types.MergePatchType, body, metav1.PatchOptions{}, "restart",
    )
    if err != nil {
        return fmt.Errorf("restarting VM %s/%s: %w", namespace, name, err)
    }

    return nil
}
```

**The restart subresource pattern.** KubeVirt exposes a subresource at `/apis/kubevirt.io/v1/namespaces/<ns>/virtualmachines/<name>/restart`. This is analogous to how the core Kubernetes API exposes `/pods/<name>/log` or `/deployments/<name>/scale` as subresources. You do not modify the VM spec to restart it -- you hit a separate API endpoint that triggers the action.

In the dynamic client, the `"restart"` string passed as the final variadic argument to `Patch` tells the client to append `/restart` to the URL path. The patch body is an empty JSON object `{}` because the subresource does not need any fields -- the request itself is the signal.

This is the equivalent of running `virtctl restart <vm-name>` from the command line, but done programmatically from within the operator.

---

## The Restart Controller

With the Applier in place, we need something to decide **when** to call `RestartVM`. That is the job of the Restart Controller in `internal/controller/restart_controller.go`.

### The VMRestarter Interface

The controller does not depend on the Applier struct directly. Instead, it depends on a narrow interface:

```go
type VMRestarter interface {
    RestartVM(ctx context.Context, name, namespace string) error
}
```

This is a textbook example of the **Interface Segregation Principle**. The Restart Controller only needs one operation -- restarting a VM -- so it declares an interface with just that method. The Applier satisfies this interface, but so does any test double. This makes unit testing the controller straightforward: inject a mock that records whether `RestartVM` was called and with what arguments.

### The Reconciler Struct

```go
type RestartReconciler struct {
    client.Client
    Scheme  *runtime.Scheme
    Applier VMRestarter
    Log     logr.Logger
}
```

The struct embeds `client.Client` for reading and updating `RightsizingRecommendation` resources, holds a `VMRestarter` for triggering restarts, and carries a structured logger. This is a standard `controller-runtime` reconciler shape.

### The Reconcile Flow

The `Reconcile` method is where the scheduling logic lives. It follows a clear decision tree:

```
Fetch recommendation
        |
        v
Is state "applied-pending-restart"?
        |
   No --+--> return (do nothing)
        |
   Yes --+
        |
        v
Is ScheduledRestartAt set?
        |
   No --+--> requeue in 5 minutes
        |
   Yes --+
        |
        v
Is the scheduled time in the future?
        |
   Yes -+--> requeue at exactly that time
        |
   No --+--> trigger restart now
        |
    +---+---+
    |       |
 Success  Failure
    |       |
    v       v
 State =  State =
 Applied  Failed
```

Let us walk through each branch.

**Step 1: Fetch the recommendation.**

```go
rec := &rightsizingv1alpha1.RightsizingRecommendation{}
if err := r.Get(ctx, req.NamespacedName, rec); err != nil {
    if errors.IsNotFound(err) {
        return ctrl.Result{}, nil
    }
    return ctrl.Result{}, fmt.Errorf("fetching recommendation: %w", err)
}
```

If the resource was deleted between the time the event was queued and now, the `IsNotFound` check handles it gracefully by returning without error.

**Step 2: Filter by state.**

```go
if rec.Status.State != rightsizingv1alpha1.StateAppliedPendingRestart {
    return ctrl.Result{}, nil
}
```

The controller only cares about recommendations in the `applied-pending-restart` state. This state means the VM spec has already been patched with new resource values (by the apply endpoint in the REST API), but the VM has not yet been restarted to pick up the changes. Any other state -- `pending`, `approved`, `applied`, `reverted`, `failed` -- is ignored.

**Step 3: No scheduled time -- requeue later.**

```go
if rec.Status.ScheduledRestartAt == nil {
    return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}
```

It is possible for a recommendation to enter `applied-pending-restart` before a restart time has been set (for example, if the user applied changes through the API but has not yet chosen when to restart). In this case, the controller requeues itself to check again in 5 minutes. This is a polling fallback -- when the `ScheduledRestartAt` field is eventually set, the normal watch mechanism will also trigger a reconcile, but the requeue provides a safety net.

**Step 4: Scheduled time is in the future -- requeue precisely.**

```go
if scheduledTime.After(now) {
    requeueAfter := time.Until(scheduledTime)
    log.Info("Restart scheduled in the future", "scheduledAt", scheduledTime)
    return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
```

This is the most elegant part of the controller. Rather than polling at fixed intervals, it calculates the exact duration until the scheduled restart and asks `controller-runtime` to requeue at precisely that moment. If the restart is scheduled for 3 hours and 17 minutes from now, the controller sleeps for exactly 3 hours and 17 minutes. No wasted reconcile cycles, no late restarts.

The `time.Until(scheduledTime)` call returns the duration between `now` and `scheduledTime`, and `RequeueAfter` instructs the work queue to re-enqueue this item after that duration elapses.

**Step 5: Scheduled time has passed -- restart now.**

```go
if err := r.Applier.RestartVM(ctx, rec.Spec.VirtualMachineRef.Name, rec.Spec.VirtualMachineRef.Namespace); err != nil {
    rec.Status.State = rightsizingv1alpha1.StateFailed
    rec.Status.Message = fmt.Sprintf("restart failed: %v", err)
    _ = r.Status().Update(ctx, rec)
    return ctrl.Result{}, fmt.Errorf("restarting VM: %w", err)
}
```

When the scheduled time has arrived (or already passed), the controller calls `RestartVM` through the `VMRestarter` interface. If the restart fails, two things happen:

- The recommendation's state is set to `failed` with the error message, so the user can see what went wrong in the Console plugin.
- The error is returned to `controller-runtime`, which will retry the reconcile with exponential backoff.

Note that the status update on failure uses `_ = r.Status().Update(...)` -- the error is intentionally discarded. If the status update itself fails, we still want to return the original restart error so the framework retries the restart. The failed state will be written on the next attempt.

**Step 6: On success -- update to Applied.**

```go
nowMeta := metav1.Now()
rec.Status.State = rightsizingv1alpha1.StateApplied
rec.Status.AppliedAt = &nowMeta
rec.Status.ScheduledRestartAt = nil
if err := r.Status().Update(ctx, rec); err != nil {
    return ctrl.Result{}, fmt.Errorf("updating status after restart: %w", err)
}
```

After a successful restart, the controller:

- Sets the state to `applied` -- the recommendation is now fully enacted.
- Records `AppliedAt` with the current timestamp.
- Clears `ScheduledRestartAt` since the restart has been performed.

This is the terminal happy path. The recommendation moves from `applied-pending-restart` to `applied`, and the controller will ignore it on future reconciles because it no longer matches the state filter in Step 2.

### Registering with the Manager

```go
func (r *RestartReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&rightsizingv1alpha1.RightsizingRecommendation{}).
        Named("restart").
        Complete(r)
}
```

The controller watches `RightsizingRecommendation` resources. Any create or update event on a recommendation triggers a reconcile. The `.Named("restart")` call gives the controller a distinct name in logs and metrics, which is important because the recommendation controller also watches the same resource type -- without distinct names, `controller-runtime` would reject the second registration.

---

## How the Pieces Fit Together

Here is the end-to-end flow for applying a recommendation and restarting a VM:

1. A user approves a recommendation through the Console plugin (or REST API). The API handler calls `Applier.PatchVMResources` to write the new CPU and memory values to the VM spec.
2. The API handler sets the recommendation state to `applied-pending-restart` and records a `ScheduledRestartAt` timestamp (for example, during a maintenance window at 02:00).
3. The Restart Controller sees the state change and reconciles. It finds the scheduled time is in the future, so it requeues for exactly that time.
4. At 02:00, the controller reconciles again. The scheduled time has passed, so it calls `Applier.RestartVM`.
5. KubeVirt receives the restart subresource request, gracefully shuts down the `VirtualMachineInstance`, and boots a new one that picks up the updated CPU and memory from the `VirtualMachine` spec.
6. The controller updates the recommendation state to `applied` and clears the scheduled restart time.

The separation between the Applier (how to change a VM) and the Restart Controller (when to restart a VM) keeps each component focused on a single responsibility. The Applier knows nothing about recommendation states or scheduling. The Restart Controller knows nothing about patch formats or subresource URLs. Each can be tested, understood, and modified independently.

---

## Key Takeaways

- **The dynamic client avoids importing KubeVirt Go types**, eliminating a large transitive dependency. The trade-off is working with unstructured maps instead of typed structs, which is manageable for the small number of fields OVRO touches.

- **JSON Merge Patch** (`types.MergePatchType`) is the right choice for CRD resources. It updates only the fields you specify and leaves everything else unchanged. Strategic merge patches offer richer list-handling semantics but require struct tags that CRDs do not provide.

- **Hotplug detection via `maxSockets`** lets OVRO decide whether a restart is needed. If the VM supports CPU hotplug, some changes can be applied live. If not, a restart is required for the changes to take effect.

- **The restart subresource** is KubeVirt's API for gracefully restarting a VM without deleting and recreating it. The Applier accesses it by passing `"restart"` as a subresource argument to the dynamic client's `Patch` method.

- **Precise requeue timing** makes the Restart Controller efficient. Instead of polling at fixed intervals, it calculates the exact duration until the scheduled restart and sleeps for exactly that long, minimising unnecessary API calls.

- **Interface segregation** (`VMRestarter`) decouples the controller from the Applier implementation, making both components independently testable.

---

## Next Chapter

In [Chapter 6: REST API](06-REST-API.md), we will explore the HTTP API that the Console plugin calls to list recommendations, apply changes, schedule restarts, and manage policies -- all secured with Kubernetes-native authentication and authorisation.
