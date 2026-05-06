package approvalproxy

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kborup-redhat/ovro/internal/approval"
)

//go:embed static/*
var staticFS embed.FS

// ApprovalPageData holds the data rendered into the approval HTML template.
type ApprovalPageData struct {
	VMName            string
	Namespace         string
	Direction         string
	CurrentCPU        int32
	CurrentMemory     string
	RecommendedCPU    int32
	RecommendedMemory string
	CPUSavings        int32
	MemorySavings     string
	CPUP95            float64
	MemP95            float64
	CPUMax            float64
	MemMax            float64
	LookbackDays      int
	Token             string
	Owner             string
	HotplugCapable    bool
}

// ResultPageData holds the data rendered into the success/error result template.
type ResultPageData struct {
	Title   string
	Message string
	IsError bool
}

// backendRecommendation is a lightweight struct for parsing the backend JSON
// response. It mirrors the relevant fields from the RightsizingRecommendation CRD.
type backendRecommendation struct {
	Spec struct {
		VirtualMachineRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"virtualMachineRef"`
		Direction string `json:"direction"`
		Current   struct {
			CPU struct {
				Cores int32 `json:"cores"`
			} `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"current"`
		Recommended struct {
			CPU struct {
				Cores int32 `json:"cores"`
			} `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"recommended"`
		Savings struct {
			CPU    int32  `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"savings"`
		Metrics struct {
			LookbackDays     int     `json:"lookbackDays"`
			CPUP95Percent    float64 `json:"cpuP95Percent"`
			MemoryP95Percent float64 `json:"memoryP95Percent"`
			CPUMaxPercent    float64 `json:"cpuMaxPercent"`
			MemoryMaxPercent float64 `json:"memoryMaxPercent"`
		} `json:"metrics"`
		HotplugCapable bool `json:"hotplugCapable"`
	} `json:"spec"`
}

// ApprovalProxy validates JWT tokens and serves a standalone HTML approval page.
// It sits between the external OpenShift Route and the internal backend API.
type ApprovalProxy struct {
	tokenManager *approval.TokenManager
	backendURL   string
	httpClient   *http.Client
	mux          *http.ServeMux
	templates    *template.Template
}

// New creates a new ApprovalProxy.
func New(tokenManager *approval.TokenManager, backendURL string) *ApprovalProxy {
	p := &ApprovalProxy{
		tokenManager: tokenManager,
		backendURL:   strings.TrimRight(backendURL, "/"),
		// TODO: Accept a CA cert path as config instead of InsecureSkipVerify.
		// The backend uses a self-signed service certificate inside the cluster.
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, //nolint:gosec // internal cluster traffic with self-signed cert
				},
			},
		},
		mux: http.NewServeMux(),
	}

	tmpl, err := template.ParseFS(staticFS, "static/*.html")
	if err != nil {
		// This should never happen with embedded files — panic at startup.
		panic(fmt.Sprintf("failed to parse embedded templates: %v", err))
	}
	p.templates = tmpl

	p.mux.HandleFunc("GET /approve", p.handleGetApprove)
	p.mux.HandleFunc("POST /approve", p.handlePostApprove)
	p.mux.HandleFunc("GET /healthz", p.handleHealthz)

	return p
}

// ServeHTTP implements http.Handler.
func (p *ApprovalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mux.ServeHTTP(w, r)
}

// ListenAndServe starts the proxy on the given address without TLS.
func (p *ApprovalProxy) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: p,
	}
	return srv.ListenAndServe()
}

// ListenAndServeTLS starts the proxy on the given address with TLS.
func (p *ApprovalProxy) ListenAndServeTLS(addr, certFile, keyFile string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: p,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
	}
	return srv.ListenAndServeTLS(certFile, keyFile)
}

// handleHealthz returns 200 OK for liveness/readiness probes.
func (p *ApprovalProxy) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// handleGetApprove validates the JWT token, fetches recommendation data from
// the backend, and renders the approval HTML page.
func (p *ApprovalProxy) handleGetApprove(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		p.renderError(w, http.StatusBadRequest, "Missing Token", "No approval token was provided in the request.")
		return
	}

	claims, err := p.tokenManager.ValidateToken(tokenStr)
	if err != nil {
		slog.Warn("invalid approval token", "error", err)
		p.renderError(w, http.StatusUnauthorized, "Invalid or Expired Token",
			"The approval link is invalid or has expired. Please request a new approval link.")
		return
	}

	rec, err := p.fetchRecommendation(claims.Namespace, claims.RecName)
	if err != nil {
		slog.Error("failed to fetch recommendation from backend",
			"namespace", claims.Namespace, "name", claims.RecName, "error", err)
		p.renderError(w, http.StatusBadGateway, "Unable to Load Recommendation",
			"Could not retrieve the recommendation details. Please try again later.")
		return
	}

	data := ApprovalPageData{
		VMName:            rec.Spec.VirtualMachineRef.Name,
		Namespace:         rec.Spec.VirtualMachineRef.Namespace,
		Direction:         rec.Spec.Direction,
		CurrentCPU:        rec.Spec.Current.CPU.Cores,
		CurrentMemory:     rec.Spec.Current.Memory,
		RecommendedCPU:    rec.Spec.Recommended.CPU.Cores,
		RecommendedMemory: rec.Spec.Recommended.Memory,
		CPUSavings:        rec.Spec.Savings.CPU,
		MemorySavings:     rec.Spec.Savings.Memory,
		CPUP95:            rec.Spec.Metrics.CPUP95Percent,
		MemP95:            rec.Spec.Metrics.MemoryP95Percent,
		CPUMax:            rec.Spec.Metrics.CPUMaxPercent,
		MemMax:            rec.Spec.Metrics.MemoryMaxPercent,
		LookbackDays:      rec.Spec.Metrics.LookbackDays,
		Token:             tokenStr,
		Owner:             claims.Owner,
		HotplugCapable:    rec.Spec.HotplugCapable,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := p.templates.ExecuteTemplate(w, "approval.html", data); err != nil {
		slog.Error("failed to render approval page", "error", err)
	}
}

// handlePostApprove processes the form submission from the approval page.
func (p *ApprovalProxy) handlePostApprove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.renderError(w, http.StatusBadRequest, "Invalid Form", "The submitted form data could not be parsed.")
		return
	}

	tokenStr := r.FormValue("token")
	if tokenStr == "" {
		p.renderError(w, http.StatusBadRequest, "Missing Token", "No approval token was provided.")
		return
	}

	claims, err := p.tokenManager.ValidateToken(tokenStr)
	if err != nil {
		slog.Warn("invalid approval token on POST", "error", err)
		p.renderError(w, http.StatusUnauthorized, "Invalid or Expired Token",
			"The approval link is invalid or has expired. Please request a new approval link.")
		return
	}

	action := r.FormValue("action")
	restartOption := r.FormValue("restartOption")
	scheduledAt := r.FormValue("scheduledAt")
	reason := r.FormValue("reason")

	var backendPath string
	var bodyPayload map[string]string

	switch action {
	case "approve":
		backendPath = fmt.Sprintf("/api/v1/internal/recommendations/%s/%s/owner-approve",
			claims.Namespace, claims.RecName)
		bodyPayload = map[string]string{
			"restartOption": restartOption,
		}
		if scheduledAt != "" {
			bodyPayload["scheduledAt"] = scheduledAt
		}
	case "reject":
		backendPath = fmt.Sprintf("/api/v1/internal/recommendations/%s/%s/owner-reject",
			claims.Namespace, claims.RecName)
		bodyPayload = map[string]string{
			"reason": reason,
		}
	case "exclude":
		backendPath = fmt.Sprintf("/api/v1/internal/recommendations/%s/%s/owner-reject",
			claims.Namespace, claims.RecName)
		bodyPayload = map[string]string{
			"reason":  reason,
			"exclude": "true",
		}
	default:
		p.renderError(w, http.StatusBadRequest, "Invalid Action",
			"The selected action is not valid. Please go back and try again.")
		return
	}

	bodyJSON, err := json.Marshal(bodyPayload)
	if err != nil {
		slog.Error("failed to marshal request body", "error", err)
		p.renderError(w, http.StatusInternalServerError, "Internal Error",
			"An unexpected error occurred. Please try again later.")
		return
	}

	backendReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		p.backendURL+backendPath, strings.NewReader(string(bodyJSON)))
	if err != nil {
		slog.Error("failed to create backend request", "error", err)
		p.renderError(w, http.StatusInternalServerError, "Internal Error",
			"An unexpected error occurred. Please try again later.")
		return
	}
	backendReq.Header.Set("Content-Type", "application/json")
	backendReq.Header.Set("X-Approval-Owner", claims.Owner)

	resp, err := p.httpClient.Do(backendReq)
	if err != nil {
		slog.Error("backend request failed", "path", backendPath, "error", err)
		p.renderError(w, http.StatusBadGateway, "Backend Unavailable",
			"Could not reach the backend service. Please try again later.")
		return
	}
	defer resp.Body.Close()
	// Discard the response body to allow connection reuse.
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	if resp.StatusCode >= 400 {
		slog.Error("backend returned error", "path", backendPath, "status", resp.StatusCode)
		p.renderError(w, http.StatusBadGateway, "Action Failed",
			fmt.Sprintf("The backend returned an error (HTTP %d). Please try again or contact your administrator.", resp.StatusCode))
		return
	}

	var title, message string
	switch action {
	case "approve":
		title = "Recommendation Approved"
		message = "The rightsizing recommendation has been approved and will be applied."
		if restartOption == "schedule" && scheduledAt != "" {
			message += fmt.Sprintf(" The VM restart is scheduled for %s.", scheduledAt)
		} else if restartOption == "later" {
			message += " The VM will need to be restarted manually to apply the changes."
		}
	case "reject":
		title = "Recommendation Rejected"
		message = "The rightsizing recommendation has been rejected."
	case "exclude":
		title = "VM Excluded from Rightsizing"
		message = "The VM has been excluded from future rightsizing recommendations."
	}

	p.renderResult(w, title, message)
}

// fetchRecommendation retrieves a recommendation from the backend API.
func (p *ApprovalProxy) fetchRecommendation(namespace, name string) (*backendRecommendation, error) {
	url := fmt.Sprintf("%s/api/v1/recommendations/%s/%s", p.backendURL, namespace, name)
	resp, err := p.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	var rec backendRecommendation
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return nil, fmt.Errorf("decoding recommendation: %w", err)
	}
	return &rec, nil
}

// renderError renders the error HTML page.
func (p *ApprovalProxy) renderError(w http.ResponseWriter, statusCode int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := p.templates.ExecuteTemplate(w, "error.html", ResultPageData{
		Title:   title,
		Message: message,
		IsError: true,
	}); err != nil {
		slog.Error("failed to render error page", "error", err)
		http.Error(w, title, statusCode)
	}
}

// renderResult renders the success HTML page.
func (p *ApprovalProxy) renderResult(w http.ResponseWriter, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := p.templates.ExecuteTemplate(w, "result.html", ResultPageData{
		Title:   title,
		Message: message,
	}); err != nil {
		slog.Error("failed to render result page", "error", err)
	}
}
