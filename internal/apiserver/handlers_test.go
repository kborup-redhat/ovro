package apiserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/apiserver"
	"github.com/kborup-redhat/ovro/internal/controller"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeK8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func allowAllClientset() *fakeK8s.Clientset {
	cs := fakeK8s.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tr := action.(k8stesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status = authenticationv1.TokenReviewStatus{
			Authenticated: true,
			User: authenticationv1.UserInfo{
				Username: "test-user",
				UID:      "uid-123",
				Groups:   []string{"system:authenticated"},
			},
		}
		return true, tr, nil
	})
	cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		sar := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		sar.Status = authv1.SubjectAccessReviewStatus{Allowed: true}
		return true, sar, nil
	})
	return cs
}

func setupTestServer(objects ...client.Object) *apiserver.Server {
	scheme := controller.SetupScheme()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&rightsizingv1alpha1.RightsizingRecommendation{}).
		Build()
	return apiserver.NewServer(k8sClient, allowAllClientset(), nil)
}

func authedRequest(method, url string, body []byte) *http.Request {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, url, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func TestHandleListRecommendations(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
			Direction:         rightsizingv1alpha1.DirectionDownsize,
			Current:           rightsizingv1alpha1.ResourceSpec{CPU: rightsizingv1alpha1.CPUSpec{Cores: 8}, Memory: resource.MustParse("16Gi")},
			Recommended:       rightsizingv1alpha1.ResourceSpec{CPU: rightsizingv1alpha1.CPUSpec{Cores: 4}, Memory: resource.MustParse("8Gi")},
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{State: rightsizingv1alpha1.StatePending},
	}

	server := setupTestServer(rec)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodGet, "/api/v1/recommendations", nil))

	assert.Equal(t, http.StatusOK, w.Code)

	var items []rightsizingv1alpha1.RightsizingRecommendation
	err := json.Unmarshal(w.Body.Bytes(), &items)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestHandleGetRecommendation(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
			Direction:         rightsizingv1alpha1.DirectionDownsize,
		},
	}

	server := setupTestServer(rec)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodGet, "/api/v1/recommendations/default/vm-test", nil))

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHandleOverview(t *testing.T) {
	server := setupTestServer()
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodGet, "/api/v1/overview", nil))

	assert.Equal(t, http.StatusOK, w.Code)

	var overview apiserver.OverviewResponse
	err := json.Unmarshal(w.Body.Bytes(), &overview)
	require.NoError(t, err)
}

func TestHandleApply(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
			Direction:         rightsizingv1alpha1.DirectionDownsize,
			HotplugCapable:    true,
			Current:           rightsizingv1alpha1.ResourceSpec{CPU: rightsizingv1alpha1.CPUSpec{Cores: 8}, Memory: resource.MustParse("16Gi")},
			Recommended:       rightsizingv1alpha1.ResourceSpec{CPU: rightsizingv1alpha1.CPUSpec{Cores: 4}, Memory: resource.MustParse("8Gi")},
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{State: rightsizingv1alpha1.StatePending},
	}

	server := setupTestServer(rec)
	body, _ := json.Marshal(apiserver.ApplyRequest{RestartOption: "now"})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPost, "/api/v1/recommendations/default/vm-test/apply", body))

	assert.Equal(t, http.StatusOK, w.Code)

	var result rightsizingv1alpha1.RightsizingRecommendation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, rightsizingv1alpha1.StateApplied, result.Status.State)
	assert.NotNil(t, result.Status.RevertConfig)
}

func TestHandleApply_NotPending(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
			Direction:         rightsizingv1alpha1.DirectionDownsize,
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{State: rightsizingv1alpha1.StateApplied},
	}

	server := setupTestServer(rec)
	body, _ := json.Marshal(apiserver.ApplyRequest{RestartOption: "now"})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPost, "/api/v1/recommendations/default/vm-test/apply", body))

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleRevert(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
			Direction:         rightsizingv1alpha1.DirectionDownsize,
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{
			State: rightsizingv1alpha1.StateApplied,
			RevertConfig: &rightsizingv1alpha1.ResourceSpec{
				CPU:    rightsizingv1alpha1.CPUSpec{Cores: 8},
				Memory: resource.MustParse("16Gi"),
			},
		},
	}

	server := setupTestServer(rec)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPost, "/api/v1/recommendations/default/vm-test/revert", nil))

	assert.Equal(t, http.StatusOK, w.Code)

	var result rightsizingv1alpha1.RightsizingRecommendation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, rightsizingv1alpha1.StateReverted, result.Status.State)
}

func TestHandleRevert_NotApplied(t *testing.T) {
	rec := &rightsizingv1alpha1.RightsizingRecommendation{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-test", Namespace: "default"},
		Spec: rightsizingv1alpha1.RightsizingRecommendationSpec{
			VirtualMachineRef: rightsizingv1alpha1.ObjectRef{Name: "test", Namespace: "default"},
		},
		Status: rightsizingv1alpha1.RightsizingRecommendationStatus{State: rightsizingv1alpha1.StatePending},
	}

	server := setupTestServer(rec)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPost, "/api/v1/recommendations/default/vm-test/revert", nil))

	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestHandleUpdatePolicy_InvalidInput(t *testing.T) {
	policy := &rightsizingv1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       rightsizingv1alpha1.DefaultPolicySpec(),
	}

	server := setupTestServer(policy)
	body := `{"lookbackDays": -1, "algorithm": {"percentile": 95, "headroomPercent": 20}}`
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPut, "/api/v1/policy", []byte(body)))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "lookbackDays")
}

func TestHandleUpdatePolicy_InvalidPercentile(t *testing.T) {
	policy := &rightsizingv1alpha1.RightsizingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       rightsizingv1alpha1.DefaultPolicySpec(),
	}

	server := setupTestServer(policy)
	body := `{"lookbackDays": 14, "algorithm": {"percentile": 200, "headroomPercent": 20}}`
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPut, "/api/v1/policy", []byte(body)))

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "percentile")
}

func TestHandleErrors_NoInternalDetails(t *testing.T) {
	server := setupTestServer()
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodGet, "/api/v1/recommendations/default/nonexistent", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotContains(t, w.Body.String(), "client.ObjectKey")
	assert.NotContains(t, w.Body.String(), "rightsizingrecommendations")
}

func TestHandleBodySizeLimit(t *testing.T) {
	server := setupTestServer()
	largeBody := strings.Repeat("x", 2<<20)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, authedRequest(http.MethodPost, "/api/v1/recommendations/default/vm-test/apply", []byte(largeBody)))

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
