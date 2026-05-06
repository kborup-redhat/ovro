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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VMRestarter abstracts VM restart operations for testability.
type VMRestarter interface {
	RestartVM(ctx context.Context, name, namespace string) error
}

// RestartReconciler watches RightsizingRecommendation resources in the
// applied-pending-restart state and triggers VM restarts when the scheduled
// time arrives.
type RestartReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Applier VMRestarter
	Log     logr.Logger
}

// +kubebuilder:rbac:groups=rightsizing.redhatconsulting.io,resources=rightsizingrecommendations,verbs=get;list;watch
// +kubebuilder:rbac:groups=rightsizing.redhatconsulting.io,resources=rightsizingrecommendations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch

// Reconcile checks whether a RightsizingRecommendation in applied-pending-restart
// state has reached its scheduled restart time, and if so, restarts the VM.
func (r *RestartReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("recommendation", req.NamespacedName)

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := r.Get(ctx, req.NamespacedName, rec); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching recommendation: %w", err)
	}

	// Only act on recommendations in the applied-pending-restart state.
	if rec.Status.State != rightsizingv1alpha1.StateAppliedPendingRestart {
		return ctrl.Result{}, nil
	}

	// If no restart time is scheduled, requeue and check later.
	if rec.Status.ScheduledRestartAt == nil {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	now := time.Now()
	scheduledTime := rec.Status.ScheduledRestartAt.Time

	// If the scheduled time is in the future, requeue to fire at the right moment.
	if scheduledTime.After(now) {
		requeueAfter := time.Until(scheduledTime)
		log.Info("Restart scheduled in the future", "scheduledAt", scheduledTime)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Scheduled time has passed — trigger the restart.
	log.Info("Triggering scheduled restart", "vm", rec.Spec.VirtualMachineRef.Name)
	if err := r.Applier.RestartVM(ctx, rec.Spec.VirtualMachineRef.Name, rec.Spec.VirtualMachineRef.Namespace); err != nil {
		rec.Status.State = rightsizingv1alpha1.StateFailed
		rec.Status.Message = fmt.Sprintf("restart failed: %v", err)
		_ = r.Status().Update(ctx, rec)
		return ctrl.Result{}, fmt.Errorf("restarting VM: %w", err)
	}

	// Update status to reflect successful restart.
	nowMeta := metav1.Now()
	rec.Status.State = rightsizingv1alpha1.StateApplied
	rec.Status.AppliedAt = &nowMeta
	rec.Status.ScheduledRestartAt = nil
	if err := r.Status().Update(ctx, rec); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status after restart: %w", err)
	}

	log.Info("Scheduled restart completed successfully")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RestartReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rightsizingv1alpha1.RightsizingRecommendation{}).
		Named("restart").
		Complete(r)
}
