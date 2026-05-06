package applier_test

import (
	"context"
	"testing"

	"github.com/kborup-redhat/ovro/internal/applier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newFakeVM(name, namespace string, cores int64, memoryGi string, maxSockets int64) *unstructured.Unstructured {
	vm := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kubevirt.io/v1",
			"kind":       "VirtualMachine",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"domain": map[string]interface{}{
							"cpu": map[string]interface{}{
								"cores":   cores,
								"sockets": int64(1),
								"threads": int64(1),
							},
							"memory": map[string]interface{}{
								"guest": memoryGi,
							},
						},
					},
				},
			},
		},
	}

	if maxSockets > 0 {
		_ = unstructured.SetNestedField(vm.Object, maxSockets, "spec", "template", "spec", "domain", "cpu", "maxSockets")
	}

	return vm
}

func TestDetectHotplugCapable(t *testing.T) {
	vm := newFakeVM("test-vm", "default", 4, "8Gi", 16)
	assert.True(t, applier.IsHotplugCapable(vm))
}

func TestDetectNotHotplugCapable(t *testing.T) {
	vm := newFakeVM("test-vm", "default", 4, "8Gi", 0)
	assert.False(t, applier.IsHotplugCapable(vm))
}

func TestPatchVMResources(t *testing.T) {
	scheme := runtime.NewScheme()
	vm := newFakeVM("test-vm", "default", 8, "16Gi", 0)

	gvr := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	client := dynamicfake.NewSimpleDynamicClient(scheme, vm)

	a := applier.New(client)
	err := a.PatchVMResources(context.Background(), "test-vm", "default", 4, resource.MustParse("8Gi"))

	require.NoError(t, err)

	updated, err := client.Resource(gvr).Namespace("default").Get(context.Background(), "test-vm", metav1.GetOptions{})
	require.NoError(t, err)

	cores, _, _ := unstructured.NestedInt64(updated.Object, "spec", "template", "spec", "domain", "cpu", "cores")
	assert.Equal(t, int64(4), cores)
}
