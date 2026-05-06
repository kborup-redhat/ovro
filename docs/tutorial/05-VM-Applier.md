---
title: "Chapter 5: VM Applier"
order: 5
---

# Chapter 5: VM Applier

## Introduction

The VM Applier is the component that actually changes virtual machines. When a recommendation is approved and applied, the Applier patches the KubeVirt `VirtualMachine` resource to update its CPU cores and memory. It can also trigger VM restarts for non-hotplug VMs. Think of it as the mechanic who does the actual work after the inspector (calculator) and manager (controller) have agreed on what needs to be done.

## How It Works

The Applier uses the Kubernetes dynamic client rather than a typed KubeVirt client. This means OVRO doesn't need to import the entire KubeVirt Go module — it just patches JSON paths on unstructured objects.

```go
// internal/applier/applier.go

var vmGVR = schema.GroupVersionResource{
    Group:    "kubevirt.io",
    Version:  "v1",
    Resource: "virtualmachines",
}

type Applier struct {
    dynamicClient dynamic.Interface
}

func New(dynamicClient dynamic.Interface) *Applier {
    return &Applier{dynamicClient: dynamicClient}
}
```

## Patching VM Resources

The core operation is `PatchVMResources`, which issues a merge patch to update CPU cores and guest memory:

```go
func (a *Applier) PatchVMResources(ctx context.Context, name, namespace string,
    cpuCores int32, memory resource.Quantity) error {

    patch := map[string]interface{}{
        "spec": map[string]interface{}{
            "template": map[string]interface{}{
                "spec": map[string]interface{}{
                    "domain": map[string]interface{}{
                        "cpu": map[string]interface{}{
                            "cores": cpuCores,
                        },
                        "memory": map[string]interface{}{
                            "guest": memory.String(),
                        },
                    },
                },
            },
        },
    }

    patchBytes, _ := json.Marshal(patch)
    _, err = a.dynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
        ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
    )
    return err
}
```

This targets the KubeVirt VM spec structure:
```
spec.template.spec.domain.cpu.cores
spec.template.spec.domain.memory.guest
```

Using a merge patch means only the specified fields are updated — all other VM configuration remains untouched.

## Hotplug Detection

The Applier also detects whether a VM supports CPU hotplug:

```go
func IsHotplugCapable(vm *unstructured.Unstructured) bool {
    maxSockets, found, err := unstructured.NestedInt64(
        vm.Object, "spec", "template", "spec", "domain", "cpu", "maxSockets")
    if err != nil || !found {
        return false
    }
    return maxSockets > 0
}
```

KubeVirt enables CPU hotplug when `maxSockets` is set to a value greater than zero. When hotplug is available, changes take effect immediately without a restart. The controller uses this to decide whether to set the state to `applied` (hotplug) or `applied-pending-restart` (requires restart).

## VM Restart

For non-hotplug VMs, the Applier can trigger a restart:

```go
func (a *Applier) RestartVM(ctx context.Context, name, namespace string) error {
    body := []byte(`{}`)
    _, err := a.dynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
        ctx, name, types.MergePatchType, body, metav1.PatchOptions{}, "restart",
    )
    return err
}
```

This patches the VM's `restart` subresource, which KubeVirt interprets as a restart request. The Restart Controller (next chapter) calls this method at the scheduled time.

## Key Takeaways

- The Applier uses the dynamic client to avoid importing KubeVirt's Go types directly.
- Merge patches update only the targeted fields (CPU cores, guest memory).
- Hotplug detection checks for `maxSockets > 0` in the VM spec.
- Restart is triggered via the KubeVirt `restart` subresource.
- The Applier is a thin, focused component — it patches and restarts, nothing more.

## Next Steps

When a VM restart is scheduled for later (not "now"), something needs to watch the clock and trigger it at the right time. That's the Restart Controller.
