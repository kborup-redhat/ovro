/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/calculator"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// VMUtilization contains computed utilization percentages for a VM.
type VMUtilization struct {
	CurrentCPUCores  int32
	CurrentMemoryGiB int32
	CPUP95Percent    float64
	MemoryP95Percent float64
	CPUMaxPercent    float64
	MemoryMaxPercent float64
}

// PrometheusQuerier abstracts Prometheus queries for testability.
type PrometheusQuerier interface {
	GetVMUtilization(ctx context.Context, vmName, namespace string, lookbackDays int) (*VMUtilization, error)
}

// getVMResources fetches the current CPU cores and memory from a VirtualMachine object.
func (r *RightsizingRecommendationReconciler) getVMResources(ctx context.Context, name, namespace string) (cpuCores int32, memoryGiB int32, err error) {
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"})
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, vm); err != nil {
		return 0, 0, fmt.Errorf("fetching VM: %w", err)
	}

	cores, found, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "cores")
	if found {
		cpuCores = int32(cores)
	}

	memStr, found, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "domain", "resources", "requests", "memory")
	if found {
		q, parseErr := resource.ParseQuantity(memStr)
		if parseErr == nil {
			memoryGiB = int32(q.Value() / (1024 * 1024 * 1024))
		}
	}

	return cpuCores, memoryGiB, nil
}

// RightsizingRecommendationReconciler reconciles VM utilization data into
// RightsizingRecommendation resources.
type RightsizingRecommendationReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	PromClient PrometheusQuerier
	Log        logr.Logger
}

// +kubebuilder:rbac:groups=rightsizing.redhatconsulting.io,resources=rightsizingrecommendations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rightsizing.redhatconsulting.io,resources=rightsizingrecommendations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rightsizing.redhatconsulting.io,resources=rightsizingpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch

// Reconcile fetches VM utilization from Prometheus, runs the rightsizing
// calculator, and creates or updates a RightsizingRecommendation resource.
func (r *RightsizingRecommendationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("vm", req.NamespacedName)

	// Fetch the cluster-wide RightsizingPolicy (or use defaults).
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

	// Fetch VM current resources from the VirtualMachine object.
	cpuCores, memoryGiB, err := r.getVMResources(ctx, req.Name, req.Namespace)
	if err != nil {
		log.Error(err, "Failed to fetch VM resources")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Query Prometheus for VM utilization metrics.
	utilization, err := r.PromClient.GetVMUtilization(ctx, req.Name, req.Namespace, policy.Spec.LookbackDays)
	if err != nil {
		log.Error(err, "Failed to query Prometheus")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	utilization.CurrentCPUCores = cpuCores
	utilization.CurrentMemoryGiB = memoryGiB

	// Run the rightsizing calculator.
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

	// Create or update the RightsizingRecommendation resource.
	now := metav1.Now()
	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	recName := types.NamespacedName{Name: "vm-" + req.Name, Namespace: req.Namespace}
	err = r.Get(ctx, recName, rec)

	if errors.IsNotFound(err) {
		rec = &rightsizingv1alpha1.RightsizingRecommendation{
			ObjectMeta: metav1.ObjectMeta{
				Name:      recName.Name,
				Namespace: recName.Namespace,
			},
			Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
				VirtualMachineRef: rightsizingv1alpha1.ObjectRef{
					Name:      req.Name,
					Namespace: req.Namespace,
				},
				Direction: rightsizingv1alpha1.RecommendationDirection(result.Direction),
				Current: rightsizingv1alpha1.ResourceSpec{
					CPU:    rightsizingv1alpha1.CPUSpec{Cores: input.CurrentCPUCores, Sockets: 1, Threads: 1},
					Memory: resource.MustParse(fmt.Sprintf("%dGi", input.CurrentMemoryGiB)),
				},
				Recommended: rightsizingv1alpha1.ResourceSpec{
					CPU:    rightsizingv1alpha1.CPUSpec{Cores: result.RecommendedCPUCores, Sockets: 1, Threads: 1},
					Memory: resource.MustParse(fmt.Sprintf("%dGi", result.RecommendedMemoryGiB)),
				},
				Savings: rightsizingv1alpha1.SavingsSpec{
					CPU:    result.CPUSavings,
					Memory: resource.MustParse(fmt.Sprintf("%dGi", abs(result.MemorySavings))),
				},
				Metrics: rightsizingv1alpha1.MetricsSnapshot{
					LookbackDays:     policy.Spec.LookbackDays,
					CPUP95Percent:    utilization.CPUP95Percent,
					MemoryP95Percent: utilization.MemoryP95Percent,
					CPUMaxPercent:    utilization.CPUMaxPercent,
					MemoryMaxPercent: utilization.MemoryMaxPercent,
				},
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

		log.Info("Created recommendation", "direction", result.Direction)
	} else if err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching recommendation: %w", err)
	} else {
		// Skip update if already applied.
		if rec.Status.State == rightsizingv1alpha1.StateApplied ||
			rec.Status.State == rightsizingv1alpha1.StateAppliedPendingRestart {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}

		rec.Spec.Direction = rightsizingv1alpha1.RecommendationDirection(result.Direction)
		rec.Spec.Recommended = rightsizingv1alpha1.ResourceSpec{
			CPU:    rightsizingv1alpha1.CPUSpec{Cores: result.RecommendedCPUCores, Sockets: 1, Threads: 1},
			Memory: resource.MustParse(fmt.Sprintf("%dGi", result.RecommendedMemoryGiB)),
		}
		rec.Spec.Savings = rightsizingv1alpha1.SavingsSpec{
			CPU:    result.CPUSavings,
			Memory: resource.MustParse(fmt.Sprintf("%dGi", abs(result.MemorySavings))),
		}

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

// SetupWithManager sets up the controller with the Manager.
func (r *RightsizingRecommendationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	vmGVK := schema.GroupVersionKind{Group: "kubevirt.io", Version: "v1", Kind: "VirtualMachine"}
	vm := &unstructured.Unstructured{}
	vm.SetGroupVersionKind(vmGVK)

	return ctrl.NewControllerManagedBy(mgr).
		Named("rightsizingrecommendation").
		WatchesRawSource(source.Kind(mgr.GetCache(), vm, &handler.TypedEnqueueRequestForObject[*unstructured.Unstructured]{})).
		Complete(r)
}

func defaultPolicy() *rightsizingv1alpha1.RightsizingPolicy {
	return &rightsizingv1alpha1.RightsizingPolicy{
		Spec: rightsizingv1alpha1.DefaultPolicySpec(),
	}
}

func abs(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}

// SetupScheme creates a runtime.Scheme with the rightsizing API types registered,
// for use in tests with fake clients.
func SetupScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = rightsizingv1alpha1.AddToScheme(scheme)
	return scheme
}
