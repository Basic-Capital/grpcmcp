package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"

	"connectrpc.com/connect"
)

// streamableEndpointPath is where mcp-go mounts the streamable HTTP server.
// It matches the default that StreamableHTTPServer.Start uses.
const streamableEndpointPath = "/mcp"

// certPoolFromFile reads a PEM bundle into a certificate pool.
func certPoolFromFile(path string) (*x509.CertPool, error) {
	pems, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pems) {
		return nil, fmt.Errorf("no certificates found in CA file %q", path)
	}
	return pool, nil
}

// backendTLSClient builds the HTTP client for outbound backend calls when any
// backend TLS option is set. It returns a nil client when no option is set, so
// the caller keeps the default behavior.
//
// caFile supplies the roots used to verify the backend certificate. certFile
// and keyFile present a client certificate to the backend for mTLS.
func backendTLSClient(caFile string, certFile string, keyFile string) (connect.HTTPClient, error) {
	if caFile == "" && certFile == "" && keyFile == "" {
		return nil, nil
	}
	var cfg tls.Config
	if caFile != "" {
		pool, err := certPoolFromFile(caFile)
		if err != nil {
			return nil, err
		}
		// RootCAs verifies the server we dial. ClientCAs is the server-side
		// field and has no effect on an outbound client.
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("backend client certificate needs both -client-tls-crt and -client-tls-key")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate (%s, %s): %w", certFile, keyFile, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	// Start from DefaultTransport and change only the TLS settings. A transport
	// built from an empty literal drops every default: Proxy, so HTTPS_PROXY and
	// NO_PROXY stop working; the dial and handshake timeouts; and the idle
	// connection limits, which leaves an idle connection open for the life of the
	// process.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &cfg
	// A custom TLSClientConfig turns off the automatic HTTP/2 upgrade. gRPC needs
	// HTTP/2, so ask for it explicitly.
	transport.ForceAttemptHTTP2 = true
	return &http.Client{Transport: transport}, nil
}

// serveTLS serves handler over TLS on addr. caFile, when set, supplies the
// roots used to verify inbound client certificates, which requires every client
// to present one.
func serveTLS(handler http.Handler, addr string, caFile string, certFile string, keyFile string) error {
	var cfg tls.Config
	if caFile != "" {
		pool, err := certPoolFromFile(caFile)
		if err != nil {
			return err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	httpSrv := &http.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: &cfg,
	}
	// ListenAndServeTLS, not ListenAndServe: the latter ignores TLSConfig and
	// serves plaintext.
	return httpSrv.ListenAndServeTLS(certFile, keyFile)
}
