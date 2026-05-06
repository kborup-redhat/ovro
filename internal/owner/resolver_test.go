package owner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
)

func TestResolveOwner_FoundOnVM(t *testing.T) {
	// Setup
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rightsizingv1alpha1.AddToScheme(s))

	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubevirt.io",
		Version: "v1",
		Kind:    "VirtualMachine",
	})
	vm.SetName("test-vm")
	vm.SetNamespace("test-ns")
	vm.SetLabels(map[string]string{
		rightsizingv1alpha1.LabelOwner: "owner@example.com",
	})

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(vm, ns).
		Build()

	resolver := &Resolver{Client: fakeClient}

	// Execute
	owner, err := resolver.ResolveOwner(context.Background(), "test-vm", "test-ns")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "owner@example.com", owner)
}

func TestResolveOwner_FoundOnNamespace(t *testing.T) {
	// Setup
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rightsizingv1alpha1.AddToScheme(s))

	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubevirt.io",
		Version: "v1",
		Kind:    "VirtualMachine",
	})
	vm.SetName("test-vm")
	vm.SetNamespace("test-ns")
	// No owner label on VM

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			Labels: map[string]string{
				rightsizingv1alpha1.LabelOwner: "namespace-owner@example.com",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(vm, ns).
		Build()

	resolver := &Resolver{Client: fakeClient}

	// Execute
	owner, err := resolver.ResolveOwner(context.Background(), "test-vm", "test-ns")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "namespace-owner@example.com", owner)
}

func TestResolveOwner_NotFoundAnywhere(t *testing.T) {
	// Setup
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rightsizingv1alpha1.AddToScheme(s))

	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubevirt.io",
		Version: "v1",
		Kind:    "VirtualMachine",
	})
	vm.SetName("test-vm")
	vm.SetNamespace("test-ns")
	// No owner label on VM

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			// No owner label on namespace
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(vm, ns).
		Build()

	resolver := &Resolver{Client: fakeClient}

	// Execute
	owner, err := resolver.ResolveOwner(context.Background(), "test-vm", "test-ns")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "", owner)
}

func TestResolveOwner_VMNotFound(t *testing.T) {
	// Setup
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rightsizingv1alpha1.AddToScheme(s))

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ns).
		Build()

	resolver := &Resolver{Client: fakeClient}

	// Execute
	owner, err := resolver.ResolveOwner(context.Background(), "nonexistent-vm", "test-ns")

	// Assert
	require.Error(t, err)
	assert.Equal(t, "", owner)
}
