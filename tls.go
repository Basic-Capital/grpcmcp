package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

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
		reloader, err := newCertReloader(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		cfg.GetClientCertificate = reloader.GetClientCertificate
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

type certReloader struct {
	certFile string
	keyFile  string
	now      func() time.Time
	log      io.Writer

	mu          sync.Mutex
	cert        *tls.Certificate
	certMod     time.Time
	keyMod      time.Time
	lastFailure string
}

func newCertReloader(certFile string, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile, now: time.Now, log: os.Stderr}
	if _, err := r.certificate(); err != nil {
		return nil, err
	}
	return r, nil
}

var errCertificateUnavailable = errors.New("TLS certificate is unavailable; the grpcmcp log has the cause")

func (r *certReloader) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return r.handshakeCertificate()
}

func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.handshakeCertificate()
}

func (r *certReloader) handshakeCertificate() (*tls.Certificate, error) {
	cert, err := r.certificate()
	if err != nil {
		return nil, errCertificateUnavailable
	}
	return cert, nil
}

func (r *certReloader) certificate() (*tls.Certificate, error) {
	r.mu.Lock()
	cert, message, err := r.reload()
	r.mu.Unlock()

	if message != "" {
		fmt.Fprint(r.log, message)
	}
	return cert, err
}

func (r *certReloader) reload() (*tls.Certificate, string, error) {
	certInfo, certErr := os.Stat(r.certFile)
	keyInfo, keyErr := os.Stat(r.keyFile)
	statOK := certErr == nil && keyErr == nil
	if statOK && r.cert != nil && certInfo.ModTime().Equal(r.certMod) && keyInfo.ModTime().Equal(r.keyMod) {
		return r.cert, "", nil
	}

	cert, err := loadKeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert == nil {
			return nil, "", err
		}
		expiry := r.cert.Leaf.NotAfter.Format(time.RFC3339)
		if !r.now().Before(r.cert.Leaf.NotAfter) {
			return nil, r.newFailure(fmt.Sprintf("%v; the previous certificate expired %s.\n", err, expiry)), err
		}
		return r.cert, r.newFailure(fmt.Sprintf("%v; using the previous certificate, which expires %s.\n", err, expiry)), nil
	}
	var message string
	if r.cert == nil || string(r.cert.Certificate[0]) != string(cert.Certificate[0]) {
		message = fmt.Sprintf("Loaded certificate %s, which expires %s.\n", r.certFile, cert.Leaf.NotAfter.Format(time.RFC3339))
	}
	r.lastFailure = ""
	r.cert = &cert
	if statOK {
		r.certMod = certInfo.ModTime()
		r.keyMod = keyInfo.ModTime()
	}
	return r.cert, message, nil
}

func (r *certReloader) newFailure(message string) string {
	if message == r.lastFailure {
		return ""
	}
	r.lastFailure = message
	return message
}

func loadKeyPair(certFile string, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load certificate (%s, %s): %w", certFile, keyFile, err)
	}
	if cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("parse certificate %s: %w", certFile, err)
		}
		cert.Leaf = leaf
	}
	return cert, nil
}

// serverTLSConfig builds the TLS config this server listens with. caFile, when
// set, supplies the roots used to verify inbound client certificates.
//
// ClientCAs is the server-side field, and it verifies the client we accept.
// RootCAs would have no effect here, which is the reverse of the outbound client
// in backendTLSClient. ClientAuth must also be set: a pool on its own verifies
// nothing, because the server never asks for a certificate.
func serverTLSConfig(caFile string) (*tls.Config, error) {
	var cfg tls.Config
	if caFile != "" {
		pool, err := certPoolFromFile(caFile)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return &cfg, nil
}

// serveTLS serves handler over TLS on addr. caFile, when set, requires every
// client to present a certificate that the roots in caFile verify.
func serveTLS(handler http.Handler, addr string, caFile string, certFile string, keyFile string) error {
	cfg, err := serverTLSConfig(caFile)
	if err != nil {
		return err
	}
	reloader, err := newCertReloader(certFile, keyFile)
	if err != nil {
		return err
	}
	cfg.GetCertificate = reloader.GetCertificate
	httpSrv := &http.Server{
		Addr:      addr,
		Handler:   handler,
		TLSConfig: cfg,
	}
	// ListenAndServeTLS, not ListenAndServe: the latter ignores TLSConfig and
	// serves plaintext. The empty file arguments are intentional: the server
	// certificate comes from TLSConfig.GetCertificate, which reloads it after a
	// rotation. File paths here would also load the certificate once into
	// TLSConfig.Certificates, and crypto/tls serves that fixed certificate to a
	// client that sends no server name, such as one that dials an IP address.
	return httpSrv.ListenAndServeTLS("", "")
}
