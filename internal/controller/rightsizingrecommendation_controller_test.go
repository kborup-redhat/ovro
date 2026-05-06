package controller_test

import (
	"context"
	"testing"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type mockPrometheusClient struct {
	cpuP95     float64
	memP95     float64
	cpuMax     float64
	memMax     float64
	dataPoints int
	shouldErr  bool
}

func (m *mockPrometheusClient) GetVMUtilization(ctx context.Context, vmName, namespace string, lookbackDays int) (*controller.VMUtilization, error) {
	if m.shouldErr {
		return nil, assert.AnError
	}
	return &controller.VMUtilization{
		CPUP95Percent:    m.cpuP95,
		MemoryP95Percent: m.memP95,
		CPUMaxPercent:    m.cpuMax,
		MemoryMaxPercent: m.memMax,
		DataPoints:       m.dataPoints,
	}, nil
}

func makeVM(name, namespace string, cpuCores int64, memoryGiB string) *unstructured.Unstructured {
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
								"cores": cpuCores,
							},
							"resources": map[string]interface{}{
								"requests": map[string]interface{}{
									"memory": memoryGiB,
								},
							},
						},
					},
				},
			},
		},
	}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
	return vm
}

func TestReconcile_CreatesRecommendation(t *testing.T) {
	scheme := controller.SetupScheme()

	policy := &rightsizingv1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       rightsizingv1alpha1.DefaultPolicySpec(),
	}

	vm := makeVM("test-vm", "default", 8, "16Gi")

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, vm).
		WithStatusSubresource(&rightsizingv1alpha1.RightsizingRecommendation{}).
		Build()

	promClient := &mockPrometheusClient{
		cpuP95:     28.3,
		memP95:     41.7,
		cpuMax:     62.1,
		memMax:     58.4,
		dataPoints: 10080,
	}

	reconciler := &controller.RightsizingRecommendationReconciler{
		Client:     k8sClient,
		Scheme:     scheme,
		PromClient: promClient,
		Log:        ctrl.Log.WithName("test"),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-vm",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      "vm-test-vm",
		Namespace: "default",
	}, rec)
	require.NoError(t, err)
	assert.Equal(t, rightsizingv1alpha1.DirectionDownsize, rec.Spec.Direction)
	assert.Equal(t, rightsizingv1alpha1.StatePending, rec.Status.State)
	assert.Equal(t, int32(3), rec.Spec.Recommended.CPU.Cores)
}

func TestReconcile_NoRecommendationWhenNormalUsage(t *testing.T) {
	scheme := controller.SetupScheme()

	policy := &rightsizingv1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       rightsizingv1alpha1.DefaultPolicySpec(),
	}

	vm := makeVM("normal-vm", "default", 4, "8Gi")

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, vm).
		Build()

	promClient := &mockPrometheusClient{
		cpuP95: 75.0,
		memP95: 75.0,
		cpuMax: 85.0,
		memMax: 85.0,
	}

	reconciler := &controller.RightsizingRecommendationReconciler{
		Client:     k8sClient,
		Scheme:     scheme,
		PromClient: promClient,
		Log:        ctrl.Log.WithName("test"),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "normal-vm", Namespace: "default"},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	err = k8sClient.Get(context.Background(), types.NamespacedName{
		Name:      "vm-normal-vm",
		Namespace: "default",
	}, rec)
	assert.Error(t, err, "no recommendation should be created for normal utilization")
}

func TestReconcile_PrometheusError(t *testing.T) {
	scheme := controller.SetupScheme()

	policy := &rightsizingv1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: rightsizingv1alpha1.RightsizingPolicySpec{
			LookbackDays: 14,
			Algorithm: rightsizingv1alpha1.AlgorithmSpec{
				Percentile:      95,
				HeadroomPercent: 20,
			},
			Thresholds: rightsizingv1alpha1.ThresholdsSpec{
				MinCPUSavings:            1,
				MinMemorySavings:         resource.MustParse("1Gi"),
				UpsizeUtilizationPercent: 90,
			},
			RevertRetentionDays:      30,
			ReconcileIntervalMinutes: 60,
		},
	}

	vm := makeVM("test-vm", "default", 4, "8Gi")

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, vm).
		Build()

	promClient := &mockPrometheusClient{shouldErr: true}

	reconciler := &controller.RightsizingRecommendationReconciler{
		Client:     k8sClient,
		Scheme:     scheme,
		PromClient: promClient,
		Log:        ctrl.Log.WithName("test"),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-vm", Namespace: "default"},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0)
}
