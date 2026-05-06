package apiserver

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/notifier"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const maxBodySize = 1 << 20 // 1 MB

type OverviewResponse struct {
	TotalVMs           int   `json:"totalVMs"`
	DownsizeCandidates int   `json:"downsizeCandidates"`
	UpsizeNeeded       int   `json:"upsizeNeeded"`
	AppliedToday       int   `json:"appliedToday"`
	TotalCPUSavings    int32 `json:"totalCpuSavings"`
	TotalMemorySavings int64 `json:"totalMemorySavings"`
}

type ApplyRequest struct {
	RestartOption string `json:"restartOption"`
	ScheduledAt   string `json:"scheduledAt,omitempty"`
}

type VMListItem struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Excluded  bool   `json:"excluded"`
	Running   bool   `json:"running"`
	CPUCores  int64  `json:"cpuCores"`
	Memory    string `json:"memory"`
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encoding JSON response", "error", err)
	}
}

func (s *Server) canUserAccessNamespace(r *http.Request, user *UserInfo, namespace, verb string) bool {
	if user == nil || s.Clientset == nil {
		return false
	}
	sar := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:   user.Username,
			Groups: user.Groups,
			ResourceAttributes: &authv1.ResourceAttributes{
				Verb:      verb,
				Group:     "rightsizing.redhatconsulting.io",
				Resource:  "rightsizingrecommendations",
				Namespace: namespace,
			},
		},
	}
	result, err := s.Clientset.AuthorizationV1().SubjectAccessReviews().Create(
		r.Context(), sar, metav1.CreateOptions{},
	)
	if err != nil {
		slog.Error("namespace access check failed", "namespace", namespace, "error", err)
		return false
	}
	return result.Status.Allowed
}

func (s *Server) filterByNamespaceAccess(r *http.Request, items []rightsizingv1alpha1.RightsizingRecommendation) []rightsizingv1alpha1.RightsizingRecommendation {
	user := UserInfoFromContext(r.Context())
	if user == nil {
		return nil
	}

	checked := make(map[string]bool)
	var filtered []rightsizingv1alpha1.RightsizingRecommendation
	for _, item := range items {
		ns := item.Namespace
		allowed, exists := checked[ns]
		if !exists {
			allowed = s.canUserAccessNamespace(r, user, ns, "get")
			checked[ns] = allowed
		}
		if allowed {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s *Server) handleListRecommendations(w http.ResponseWriter, r *http.Request) {
	list := &rightsizingv1alpha1.RightsizingRecommendationList{}
	opts := []client.ListOption{}

	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	if err := s.K8sClient.List(r.Context(), list, opts...); err != nil {
		slog.Error("listing recommendations", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := s.filterByNamespaceAccess(r, list.Items)

	if direction := r.URL.Query().Get("direction"); direction != "" {
		filtered := make([]rightsizingv1alpha1.RightsizingRecommendation, 0)
		for _, item := range items {
			if string(item.Spec.Direction) == direction {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	if state := r.URL.Query().Get("state"); state != "" {
		filtered := make([]rightsizingv1alpha1.RightsizingRecommendation, 0)
		for _, item := range items {
			if string(item.Status.State) == state {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	if items == nil {
		items = []rightsizingv1alpha1.RightsizingRecommendation{}
	}

	writeJSON(w, items)
}

func (s *Server) handleGetRecommendation(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, rec)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "recommendation not found", http.StatusNotFound)
		return
	}

	if rec.Status.State != rightsizingv1alpha1.StatePending {
		http.Error(w, "recommendation is not in pending state", http.StatusConflict)
		return
	}

	// Demo mode: always simulate approval workflow with a generated token
	if s.DemoMode && s.TokenManager != nil {
		demoOwner := "demo-user@example.com"
		token, err := s.TokenManager.GenerateToken(namespace, name, demoOwner, 14*24*time.Hour)
		if err != nil {
			slog.Error("generating demo approval token", "error", err)
			http.Error(w, "failed to generate approval token", http.StatusInternalServerError)
			return
		}

		now := metav1.Now()
		rec.Status.State = rightsizingv1alpha1.StateAwaitingApproval
		rec.Status.Owner = demoOwner
		rec.Status.ApprovalToken = token
		rec.Status.NotifiedAt = &now
		rec.Status.RevertConfig = &rightsizingv1alpha1.ResourceSpec{
			CPU:    rec.Spec.Current.CPU,
			Memory: rec.Spec.Current.Memory,
		}

		if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
			slog.Error("updating recommendation for demo approval", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		approvalURL := fmt.Sprintf("https://%s/approve?token=%s", s.ApprovalRouteHost, token)

		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]interface{}{
			"status":      "awaiting-approval",
			"owner":       demoOwner,
			"message":     "Demo mode: use the approval URL to approve or reject.",
			"approvalUrl": approvalURL,
		})
		return
	}

	// Check if VM has an owner — route to approval workflow if so
	if s.OwnerResolver != nil && s.TokenManager != nil && s.Notifier != nil {
		vmName := rec.Spec.VirtualMachineRef.Name
		vmNS := rec.Spec.VirtualMachineRef.Namespace
		ownerStr, err := s.OwnerResolver.ResolveOwner(r.Context(), vmName, vmNS)
		if err != nil {
			slog.Error("resolving owner", "error", err)
		}

		if ownerStr != "" {
			token, err := s.TokenManager.GenerateToken(namespace, name, ownerStr, 14*24*time.Hour)
			if err != nil {
				slog.Error("generating approval token", "error", err)
				http.Error(w, "failed to generate approval token", http.StatusInternalServerError)
				return
			}

			now := metav1.Now()
			rec.Status.State = rightsizingv1alpha1.StateAwaitingApproval
			rec.Status.Owner = ownerStr
			rec.Status.ApprovalToken = token
			rec.Status.NotifiedAt = &now
			rec.Status.RevertConfig = &rightsizingv1alpha1.ResourceSpec{
				CPU:    rec.Spec.Current.CPU,
				Memory: rec.Spec.Current.Memory,
			}

			if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
				slog.Error("updating recommendation for approval", "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			approvalURL := fmt.Sprintf("https://%s/approve?token=%s", s.ApprovalRouteHost, token)

			notification := &notifier.Notification{
				VMName:        vmName,
				Namespace:     vmNS,
				Owner:         ownerStr,
				Direction:     string(rec.Spec.Direction),
				CurrentCPU:    rec.Spec.Current.CPU.Cores,
				CurrentMemory: int32(rec.Spec.Current.Memory.Value() / (1024 * 1024 * 1024)),
				RecCPU:        rec.Spec.Recommended.CPU.Cores,
				RecMemory:     int32(rec.Spec.Recommended.Memory.Value() / (1024 * 1024 * 1024)),
				ApprovalURL:   approvalURL,
			}
			errs := s.Notifier.SendAll(r.Context(), notification)
			for _, e := range errs {
				slog.Error("notification failed", "error", e)
			}

			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]interface{}{
				"status":      "awaiting-approval",
				"owner":       ownerStr,
				"message":     "Notification sent to owner. Awaiting approval.",
				"approvalUrl": approvalURL,
			})
			return
		}
	}

	now := metav1.Now()
	rec.Status.RevertConfig = &rightsizingv1alpha1.ResourceSpec{
		CPU:    rec.Spec.Current.CPU,
		Memory: rec.Spec.Current.Memory,
	}

	if rec.Spec.HotplugCapable {
		rec.Status.State = rightsizingv1alpha1.StateApplied
		rec.Status.AppliedAt = &now
	} else {
		rec.Status.State = rightsizingv1alpha1.StateAppliedPendingRestart
		switch req.RestartOption {
		case "schedule":
			t, err := time.Parse(time.RFC3339, req.ScheduledAt)
			if err != nil {
				http.Error(w, "invalid scheduledAt format (use RFC3339)", http.StatusBadRequest)
				return
			}
			mt := metav1.NewTime(t)
			rec.Status.ScheduledRestartAt = &mt
		case "now":
			rec.Status.AppliedAt = &now
		case "later":
			// No action — user will restart manually
		}
	}

	if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
		slog.Error("updating recommendation status", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rec)
}

func (s *Server) handleRevert(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "recommendation not found", http.StatusNotFound)
		return
	}

	if rec.Status.State != rightsizingv1alpha1.StateApplied &&
		rec.Status.State != rightsizingv1alpha1.StateAppliedPendingRestart {
		http.Error(w, "recommendation cannot be reverted in current state", http.StatusConflict)
		return
	}

	if rec.Status.RevertConfig == nil {
		http.Error(w, "no revert config available", http.StatusConflict)
		return
	}

	rec.Status.State = rightsizingv1alpha1.StateReverted
	if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
		slog.Error("reverting recommendation", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rec)
}

func (s *Server) handleExclude(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	if s.DynamicClient == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	patch := []byte(`{"metadata":{"annotations":{"` + rightsizingv1alpha1.AnnotationExclude + `":"true"}}}`)
	_, err := s.DynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
		r.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		slog.Error("excluding VM", "namespace", namespace, "name", name, "error", err)
		http.Error(w, "failed to exclude VM", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "excluded"})
}

func (s *Server) handleRemoveExclusion(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	if s.DynamicClient == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	patch := []byte(`{"metadata":{"annotations":{"` + rightsizingv1alpha1.AnnotationExclude + `":null}}}`)
	_, err := s.DynamicClient.Resource(vmGVR).Namespace(namespace).Patch(
		r.Context(), name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		slog.Error("removing VM exclusion", "namespace", namespace, "name", name, "error", err)
		http.Error(w, "failed to remove exclusion", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]string{"status": "monitoring resumed"})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	list := &rightsizingv1alpha1.RightsizingRecommendationList{}
	if err := s.K8sClient.List(r.Context(), list); err != nil {
		slog.Error("listing recommendations for overview", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	items := s.filterByNamespaceAccess(r, list.Items)

	overview := OverviewResponse{}
	overview.TotalVMs = len(items)

	today := time.Now().Truncate(24 * time.Hour)
	for _, item := range items {
		switch item.Spec.Direction {
		case rightsizingv1alpha1.DirectionDownsize:
			if item.Status.State == rightsizingv1alpha1.StatePending {
				overview.DownsizeCandidates++
				overview.TotalCPUSavings += item.Spec.Savings.CPU
				overview.TotalMemorySavings += item.Spec.Savings.Memory.Value()
			}
		case rightsizingv1alpha1.DirectionUpsize:
			if item.Status.State == rightsizingv1alpha1.StatePending {
				overview.UpsizeNeeded++
			}
		}

		if item.Status.AppliedAt != nil && item.Status.AppliedAt.After(today) {
			overview.AppliedToday++
		}
	}

	writeJSON(w, overview)
}

func (s *Server) handleListVMs(w http.ResponseWriter, r *http.Request) {
	if s.DynamicClient == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	user := UserInfoFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vmList, err := s.DynamicClient.Resource(vmGVR).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		slog.Error("listing VMs", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	checked := make(map[string]bool)
	var items []VMListItem
	for _, vm := range vmList.Items {
		ns := vm.GetNamespace()
		allowed, exists := checked[ns]
		if !exists {
			allowed = s.canUserAccessNamespace(r, user, ns, "get")
			checked[ns] = allowed
		}
		if !allowed {
			continue
		}

		annotations := vm.GetAnnotations()
		excluded := annotations[rightsizingv1alpha1.AnnotationExclude] == "true"

		printableStatus, _, _ := unstructured.NestedString(vm.Object, "status", "printableStatus")
		running := printableStatus == "Running"

		cpuCores, _, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "cores")
		memStr, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "domain", "resources", "requests", "memory")
		if memStr == "" {
			memStr = "0"
		}

		items = append(items, VMListItem{
			Name:      vm.GetName(),
			Namespace: ns,
			Excluded:  excluded,
			Running:   running,
			CPUCores:  cpuCores,
			Memory:    memStr,
		})
	}

	if items == nil {
		items = []VMListItem{}
	}
	writeJSON(w, items)
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	policy := &rightsizingv1alpha1.RightsizingPolicy{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: "default"}, policy); err != nil {
		policy = &rightsizingv1alpha1.RightsizingPolicy{
			Spec: rightsizingv1alpha1.DefaultPolicySpec(),
		}
	}

	writeJSON(w, policy)
}

func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var policySpec rightsizingv1alpha1.RightsizingPolicySpec
	if err := json.NewDecoder(r.Body).Decode(&policySpec); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if policySpec.LookbackDays <= 0 || policySpec.LookbackDays > 365 {
		http.Error(w, "lookbackDays must be between 1 and 365", http.StatusBadRequest)
		return
	}
	if policySpec.Algorithm.Percentile < 1 || policySpec.Algorithm.Percentile > 100 {
		http.Error(w, "percentile must be between 1 and 100", http.StatusBadRequest)
		return
	}
	if policySpec.Algorithm.HeadroomPercent < 0 {
		http.Error(w, "headroomPercent must be non-negative", http.StatusBadRequest)
		return
	}
	if policySpec.Thresholds.MinCPUSavings < 0 {
		http.Error(w, "minCpuSavings must be non-negative", http.StatusBadRequest)
		return
	}
	if policySpec.Thresholds.UpsizeUtilizationPercent < 1 || policySpec.Thresholds.UpsizeUtilizationPercent > 100 {
		http.Error(w, "upsizeUtilizationPercent must be between 1 and 100", http.StatusBadRequest)
		return
	}
	if policySpec.ReconcileIntervalMinutes <= 0 {
		http.Error(w, "reconcileIntervalMinutes must be greater than 0", http.StatusBadRequest)
		return
	}
	if policySpec.RevertRetentionDays <= 0 {
		http.Error(w, "revertRetentionDays must be greater than 0", http.StatusBadRequest)
		return
	}

	policy := &rightsizingv1alpha1.RightsizingPolicy{}
	key := types.NamespacedName{Name: "default"}
	if err := s.K8sClient.Get(r.Context(), key, policy); err != nil {
		http.Error(w, "policy not found", http.StatusNotFound)
		return
	}

	policy.Spec = policySpec
	if err := s.K8sClient.Update(r.Context(), policy); err != nil {
		slog.Error("updating policy", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, policy)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "recommendation not found", http.StatusNotFound)
		return
	}

	if rec.Status.State != rightsizingv1alpha1.StateAwaitingApproval {
		http.Error(w, "recommendation is not awaiting approval", http.StatusConflict)
		return
	}

	user := UserInfoFromContext(r.Context())
	now := metav1.Now()
	rec.Status.State = rightsizingv1alpha1.StatePending
	rec.Status.RejectedBy = user.Username
	rec.Status.RejectedAt = &now
	rec.Status.RejectionReason = req.Reason
	rec.Status.Owner = ""
	rec.Status.ApprovalToken = ""
	rec.Status.NotifiedAt = nil

	if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
		slog.Error("rejecting recommendation", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rec)
}

func (s *Server) handleOwnerApprove(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req ApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ownerStr := r.Header.Get("X-Approval-Owner")

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "recommendation not found", http.StatusNotFound)
		return
	}

	if rec.Status.State != rightsizingv1alpha1.StateAwaitingApproval {
		http.Error(w, "recommendation is not awaiting approval", http.StatusConflict)
		return
	}

	now := metav1.Now()
	rec.Status.ApprovedBy = ownerStr
	rec.Status.ApprovedAt = &now
	rec.Status.ApprovalToken = ""

	if rec.Spec.HotplugCapable {
		rec.Status.State = rightsizingv1alpha1.StateApplied
		rec.Status.AppliedAt = &now
	} else {
		rec.Status.State = rightsizingv1alpha1.StateAppliedPendingRestart
		switch req.RestartOption {
		case "schedule":
			t, err := time.Parse(time.RFC3339, req.ScheduledAt)
			if err != nil {
				http.Error(w, "invalid scheduledAt format", http.StatusBadRequest)
				return
			}
			mt := metav1.NewTime(t)
			rec.Status.ScheduledRestartAt = &mt
		case "now":
			rec.Status.AppliedAt = &now
		case "later":
			// Manual restart
		}
	}

	if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
		slog.Error("owner-approving recommendation", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rec)
}

func (s *Server) handleOwnerReject(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req struct {
		Reason string `json:"reason"`
		Action string `json:"action"` // "reject" or "exclude"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ownerStr := r.Header.Get("X-Approval-Owner")

	rec := &rightsizingv1alpha1.RightsizingRecommendation{}
	if err := s.K8sClient.Get(r.Context(), types.NamespacedName{Name: name, Namespace: namespace}, rec); err != nil {
		http.Error(w, "recommendation not found", http.StatusNotFound)
		return
	}

	if rec.Status.State != rightsizingv1alpha1.StateAwaitingApproval {
		http.Error(w, "recommendation is not awaiting approval", http.StatusConflict)
		return
	}

	now := metav1.Now()

	if req.Action == "exclude" {
		// Exclude the VM from rightsizing
		if s.DynamicClient != nil {
			vmName := rec.Spec.VirtualMachineRef.Name
			vmNS := rec.Spec.VirtualMachineRef.Namespace
			patch := []byte(`{"metadata":{"annotations":{"` + rightsizingv1alpha1.AnnotationExclude + `":"true"}}}`)
			_, _ = s.DynamicClient.Resource(vmGVR).Namespace(vmNS).Patch(
				r.Context(), vmName, types.MergePatchType, patch, metav1.PatchOptions{},
			)
		}
	}

	rec.Status.State = rightsizingv1alpha1.StatePending
	rec.Status.RejectedBy = ownerStr
	rec.Status.RejectedAt = &now
	rec.Status.RejectionReason = req.Reason
	rec.Status.Owner = ""
	rec.Status.ApprovalToken = ""
	rec.Status.NotifiedAt = nil

	if err := s.K8sClient.Status().Update(r.Context(), rec); err != nil {
		slog.Error("owner-rejecting recommendation", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, rec)
}
