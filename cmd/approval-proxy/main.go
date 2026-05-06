package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/kborup-redhat/ovro/internal/approval"
	"github.com/kborup-redhat/ovro/internal/approvalproxy"
)

func main() {
	var addr, backendURL, signingKeyPath, certFile, keyFile string
	flag.StringVar(&addr, "addr", ":8443", "Listen address")
	flag.StringVar(&backendURL, "backend-url", "", "Internal backend API URL")
	flag.StringVar(&signingKeyPath, "signing-key-path", "", "Path to signing key file")
	flag.StringVar(&certFile, "tls-cert", "", "TLS certificate file")
	flag.StringVar(&keyFile, "tls-key", "", "TLS key file")
	flag.Parse()

	// Allow env var overrides
	if v := os.Getenv("BACKEND_URL"); v != "" {
		backendURL = v
	}
	if v := os.Getenv("SIGNING_KEY_PATH"); v != "" {
		signingKeyPath = v
	}
	if v := os.Getenv("TLS_CERT_FILE"); v != "" {
		certFile = v
	}
	if v := os.Getenv("TLS_KEY_FILE"); v != "" {
		keyFile = v
	}

	if backendURL == "" {
		slog.Error("backend-url is required (flag or BACKEND_URL env var)")
		os.Exit(1)
	}
	if signingKeyPath == "" {
		slog.Error("signing-key-path is required (flag or SIGNING_KEY_PATH env var)")
		os.Exit(1)
	}

	signingKey, err := os.ReadFile(signingKeyPath)
	if err != nil {
		slog.Error("failed to read signing key", "path", signingKeyPath, "error", err)
		os.Exit(1)
	}

	tokenMgr := approval.NewTokenManager(signingKey)
	proxy := approvalproxy.New(tokenMgr, backendURL)

	slog.Info("Starting approval proxy", "addr", addr)
	if certFile != "" && keyFile != "" {
		err = proxy.ListenAndServeTLS(addr, certFile, keyFile)
	} else {
		err = proxy.ListenAndServe(addr)
	}
	if err != nil {
		slog.Error("proxy server error", "error", err)
		os.Exit(1)
	}
}
