package controller_test

import (
	"context"
	"testing"
	"time"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type mockApplier struct {
	restartCalled bool
}

func (m *mockApplier) RestartVM(ctx context.Context, name, namespace string) error {
	m.restartCalled = true
	return nil
}

func TestRestartController_TriggersScheduledRestart(t *testing.T) {
	scheme := controller.SetupScheme()

	pastTime := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-test",
			Namespace: "default",
		},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{
				Name:      "test",
				Namespace: "default",
			},
			Direction: rightsizingv1alpha1.DirectionDownsize,
			Current: rightsizingv1alpha1.ResourceSpec{
				CPU:    rightsizingv1alpha1.CPUSpec{Cores: 8},
				Memory: resource.MustParse("16Gi"),
			},
			Recommended: rightsizingv1alpha1.ResourceSpec{
				CPU:    rightsizingv1alpha1.CPUSpec{Cores: 4},
				Memory: resource.MustParse("8Gi"),
			},
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{
			State:              rightsizingv1alpha1.StateAppliedPendingRestart,
			ScheduledRestartAt: &pastTime,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rec).
		WithStatusSubresource(rec).
		Build()

	mock := &mockApplier{}
	reconciler := &controller.RestartReconciler{
		Client:  k8sClient,
		Scheme:  scheme,
		Applier: mock,
		Log:     ctrl.Log.WithName("test"),
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "vm-test", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.True(t, mock.restartCalled)
}

func TestRestartController_SkipsFutureRestart(t *testing.T) {
	scheme := controller.SetupScheme()

	futureTime := metav1.NewTime(time.Now().Add(24 * time.Hour))
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-test",
			Namespace: "default",
		},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{
				Name:      "test",
				Namespace: "default",
			},
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{
			State:              rightsizingv1alpha1.StateAppliedPendingRestart,
			ScheduledRestartAt: &futureTime,
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rec).
		WithStatusSubresource(rec).
		Build()

	mock := &mockApplier{}
	reconciler := &controller.RestartReconciler{
		Client:  k8sClient,
		Scheme:  scheme,
		Applier: mock,
		Log:     ctrl.Log.WithName("test"),
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "vm-test", Namespace: "default"},
	})
	require.NoError(t, err)
	assert.False(t, mock.restartCalled)
}
