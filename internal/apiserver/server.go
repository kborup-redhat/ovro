package apiserver

import (
	"crypto/tls"
	"net/http"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var vmGVR = schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

type Server struct {
	K8sClient     client.Client
	Clientset     kubernetes.Interface
	DynamicClient dynamic.Interface
	mux           *http.ServeMux
}

func NewServer(k8sClient client.Client, clientset kubernetes.Interface, dynamicClient dynamic.Interface) *Server {
	s := &Server{
		K8sClient:     k8sClient,
		Clientset:     clientset,
		DynamicClient: dynamicClient,
		mux:           http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	auth := AuthMiddleware(s.Clientset)

	s.mux.Handle("GET /api/v1/recommendations", auth(http.HandlerFunc(s.handleListRecommendations)))
	s.mux.Handle("GET /api/v1/recommendations/{namespace}/{name}", auth(http.HandlerFunc(s.handleGetRecommendation)))
	s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/apply", auth(http.HandlerFunc(s.handleApply)))
	s.mux.Handle("POST /api/v1/recommendations/{namespace}/{name}/revert", auth(http.HandlerFunc(s.handleRevert)))
	s.mux.Handle("GET /api/v1/vms", auth(http.HandlerFunc(s.handleListVMs)))
	s.mux.Handle("POST /api/v1/vms/{namespace}/{name}/exclude", auth(http.HandlerFunc(s.handleExclude)))
	s.mux.Handle("DELETE /api/v1/vms/{namespace}/{name}/exclude", auth(http.HandlerFunc(s.handleRemoveExclusion)))
	s.mux.Handle("GET /api/v1/overview", auth(http.HandlerFunc(s.handleOverview)))
	s.mux.Handle("GET /api/v1/policy", auth(http.HandlerFunc(s.handleGetPolicy)))
	s.mux.Handle("PUT /api/v1/policy", auth(http.HandlerFunc(s.handleUpdatePolicy)))
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Start(addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s,
	}
	return srv.ListenAndServe()
}

func (s *Server) StartTLS(addr, certFile, keyFile string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
	}
	return srv.ListenAndServeTLS(certFile, keyFile)
}
