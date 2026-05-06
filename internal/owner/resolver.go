package owner

import (
	"context"
	"fmt"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolver resolves the owner of a VirtualMachine from Kubernetes labels
type Resolver struct {
	Client client.Client
}

// ResolveOwner resolves the owner of a VM by checking labels on the VM first,
// then falling back to the namespace if not found on the VM.
// Returns an empty string (no error) if the owner is not found anywhere.
func (r *Resolver) ResolveOwner(ctx context.Context, vmName, namespace string) (string, error) {
	// Fetch the VirtualMachine
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubevirt.io",
		Version: "v1",
		Kind:    "VirtualMachine",
	})

	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      vmName,
		Namespace: namespace,
	}, vm)
	if err != nil {
		return "", fmt.Errorf("failed to get VirtualMachine %s/%s: %w", namespace, vmName, err)
	}

	// Check for owner label on VM
	labels := vm.GetLabels()
	if labels != nil {
		if owner, ok := labels[rightsizingv1alpha1.LabelOwner]; ok {
			return owner, nil
		}
	}

	// Owner not found on VM, check the namespace
	ns := &corev1.Namespace{}
	err = r.Client.Get(ctx, types.NamespacedName{Name: namespace}, ns)
	if err != nil {
		return "", fmt.Errorf("failed to get Namespace %s: %w", namespace, err)
	}

	// Check for owner label on namespace
	if ns.Labels != nil {
		if owner, ok := ns.Labels[rightsizingv1alpha1.LabelOwner]; ok {
			return owner, nil
		}
	}

	// Owner not found anywhere, return empty string (no error)
	return "", nil
}
