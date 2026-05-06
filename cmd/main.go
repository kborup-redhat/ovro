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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"log/slog"
	"os"
	"path/filepath"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rightsizingv1alpha1 "github.com/kborup-redhat/ovro/api/v1alpha1"
	"github.com/kborup-redhat/ovro/internal/apiserver"
	"github.com/kborup-redhat/ovro/internal/applier"
	"github.com/kborup-redhat/ovro/internal/approval"
	"github.com/kborup-redhat/ovro/internal/controller"
	"github.com/kborup-redhat/ovro/internal/notifier"
	"github.com/kborup-redhat/ovro/internal/owner"
	"github.com/kborup-redhat/ovro/internal/prometheus"
	// +kubebuilder:scaffold:imports

	"gopkg.in/yaml.v3"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(rightsizingv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

type prometheusAdapter struct {
	client *prometheus.Client
}

func (a *prometheusAdapter) GetVMUtilization(
	ctx context.Context, vmName, namespace string, lookbackDays int,
) (*controller.VMUtilization, error) {
	u, err := a.client.GetVMUtilization(ctx, vmName, namespace, lookbackDays)
	if err != nil {
		return nil, err
	}
	return &controller.VMUtilization{
		CPUP95Percent:    u.CPUP95Percent,
		MemoryP95Percent: u.MemoryP95Percent,
		CPUMaxPercent:    u.CPUMaxPercent,
		MemoryMaxPercent: u.MemoryMaxPercent,
		DataPoints:       u.DataPoints,
	}, nil
}

type apiServerRunnable struct {
	srv      *apiserver.Server
	addr     string
	certFile string
	keyFile  string
}

func (r *apiServerRunnable) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if r.certFile != "" && r.keyFile != "" {
			slog.Info("Starting API server with TLS", "addr", r.addr)
			errCh <- r.srv.StartTLS(r.addr, r.certFile, r.keyFile)
		} else {
			slog.Info("Starting API server without TLS (dev mode)", "addr", r.addr)
			errCh <- r.srv.Start(r.addr)
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

type k8sSecretGetter struct {
	client client.Client
}

func (g *k8sSecretGetter) GetSecretData(ctx context.Context, name, namespace string) (map[string][]byte, error) {
	secret := &corev1.Secret{}
	if err := g.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		return nil, err
	}
	return secret.Data, nil
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var apiAddr string
	var prometheusURL string
	var approvalRouteHost string
	var signingKeyPath string
	var notificationConfigPath string
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&apiAddr, "api-bind-address", ":8443", "The address the REST API server binds to.")
	flag.StringVar(&prometheusURL, "prometheus-url",
		"https://thanos-querier.openshift-monitoring.svc:9091",
		"The Prometheus/Thanos URL.")
	flag.StringVar(&approvalRouteHost, "approval-route-host", "", "Hostname of the approval proxy route")
	flag.StringVar(&signingKeyPath, "signing-key-path", "", "Path to JWT signing key file")
	flag.StringVar(&notificationConfigPath, "notification-config", "/etc/ovro/notifications/config.yaml",
		"Path to notification config file")
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Check for environment variable overrides
	if envHost := os.Getenv("OVRO_APPROVAL_ROUTE_HOST"); envHost != "" {
		approvalRouteHost = envHost
	}
	if envKey := os.Getenv("SIGNING_KEY_PATH"); envKey != "" {
		signingKeyPath = envKey
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "1d6cd375.redhatconsulting.io",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Create Kubernetes clients for the REST API server and controllers.
	config := ctrl.GetConfigOrDie()
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "unable to create clientset")
		os.Exit(1)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		setupLog.Error(err, "unable to create dynamic client")
		os.Exit(1)
	}

	promClient := &prometheusAdapter{client: prometheus.NewClient(prometheusURL)}
	vmApplier := applier.New(dynamicClient)

	demoMode := os.Getenv("OVRO_DEMO_MODE") == "true"
	if demoMode {
		setupLog.Info("Demo mode enabled — generating synthetic recommendations for all VMs")
	}

	// Set up the RightsizingRecommendation controller.
	if err := (&controller.RightsizingRecommendationReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		PromClient: promClient,
		Log:        ctrl.Log.WithName("controllers").WithName("Recommendation"),
		DemoMode:   demoMode,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RightsizingRecommendation")
		os.Exit(1)
	}

	// Set up the Restart controller.
	if err := (&controller.RestartReconciler{
		Client:  mgr.GetClient(),
		Scheme:  mgr.GetScheme(),
		Applier: vmApplier,
		Log:     ctrl.Log.WithName("controllers").WithName("Restart"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Restart")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	// Initialize approval workflow components (optional — skipped if not configured)
	var serverOpts []apiserver.ServerOption

	if signingKeyPath != "" {
		signingKey, err := os.ReadFile(signingKeyPath)
		if err != nil {
			setupLog.Error(err, "failed to read signing key")
			os.Exit(1)
		}

		tokenMgr := approval.NewTokenManager(signingKey)
		ownerRes := &owner.Resolver{Client: mgr.GetClient()}

		serverOpts = append(serverOpts,
			apiserver.WithTokenManager(tokenMgr),
			apiserver.WithOwnerResolver(ownerRes),
		)

		if approvalRouteHost != "" {
			serverOpts = append(serverOpts, apiserver.WithApprovalRouteHost(approvalRouteHost))
		}

		// Load notification config
		if _, err := os.Stat(notificationConfigPath); err == nil {
			configData, err := os.ReadFile(notificationConfigPath)
			if err != nil {
				setupLog.Error(err, "failed to read notification config")
				os.Exit(1)
			}

			var cfg notifier.NotifierConfig
			if err := yaml.Unmarshal(configData, &cfg); err != nil {
				setupLog.Error(err, "failed to parse notification config")
				os.Exit(1)
			}

			secretGetter := &k8sSecretGetter{client: mgr.GetClient()}
			dispatcher, err := notifier.NewDispatcher(cfg, secretGetter, ctrl.Log.WithName("notifier"))
			if err != nil {
				setupLog.Error(err, "failed to create notification dispatcher")
				os.Exit(1)
			}
			serverOpts = append(serverOpts, apiserver.WithNotifier(dispatcher))
			setupLog.Info("Notification forwarders configured")
		}
	}

	// Register the REST API server with the manager for graceful lifecycle management.
	apiSrv := apiserver.NewServer(mgr.GetClient(), clientset, dynamicClient, serverOpts...)
	if err := mgr.Add(&apiServerRunnable{
		srv:      apiSrv,
		addr:     apiAddr,
		certFile: os.Getenv("TLS_CERT_FILE"),
		keyFile:  os.Getenv("TLS_KEY_FILE"),
	}); err != nil {
		setupLog.Error(err, "unable to add API server to manager")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
