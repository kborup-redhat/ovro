package applier

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var vmGVR = schema.GroupVersionResource{
	Group:    "kubevirt.io",
	Version:  "v1",
	Resource: "virtualmachines",
}

// Applier patches KubeVirt VirtualMachine resources via the dynamic client.
type Applier struct {
	dynamicClient dynamic.Interface
}

// New creates an Applier backed by the given dynamic client.
func New(dynamicClient dynamic.Interface) *Applier {
	return &Applier{dynamicClient: dynamicClient}
}

// IsHotplugCapable returns true when the VM spec contains a positive maxSockets
// value, indicating that CPU hotplug is enabled.
func IsHotplugCapable(vm *unstructured.Unstructured) bool {
	maxSockets, found, err := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "maxSockets")
	if err != nil || !found {
		return false
	}
	return maxSockets > 0
}

// GetVM fetches a VirtualMachine by name and namespace.
func (a *Applier) GetVM(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error) {
	return a.dynamicClient.Resource(vmGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// PatchVMResources applies a merge-patch to update the CPU cores and guest
// memory of a VirtualMachine.
func (a *Applier) PatchVMResources(ctx context.Context, name, namespace string, cpuCores int32, memory resource.Quantity) error {
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

// RestartVM triggers a restart of the VirtualMachine by issuing a patch to
// its restart subresource.
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
