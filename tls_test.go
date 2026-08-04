package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedCert writes a self-signed certificate and key for "localhost"
// and returns the certificate path, the key path, and the CA bundle path. The
// certificate is its own CA here, so the same PEM serves as the trust root.
func writeSelfSignedCert(t *testing.T) (certPath string, keyPath string, caPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath, certPath
}

// TestServeTLSServesTLS guards the defect where a TLS-configured server called
// ListenAndServe and quietly served plaintext.
func TestServeTLSServesTLS(t *testing.T) {
	certPath, keyPath, caPath := writeSelfSignedCert(t)

	// Take a free port, then release it for serveTLS to bind.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	go func() {
		_ = serveTLS(handler, addr, "", certPath, keyPath)
	}()

	pool, err := certPoolFromFile(caPath)
	if err != nil {
		t.Fatalf("certPoolFromFile: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	var resp *http.Response
	for range 50 {
		resp, err = client.Get("https://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTPS request failed, so the listener is not serving TLS: %v", err)
	}
	defer resp.Body.Close()
	if resp.TLS == nil {
		t.Fatal("connection carried no TLS state")
	}
}

func TestBackendTLSClient(t *testing.T) {
	certPath, keyPath, caPath := writeSelfSignedCert(t)

	t.Run("no options returns no client", func(t *testing.T) {
		c, err := backendTLSClient("", "", "")
		if err != nil {
			t.Fatalf("backendTLSClient: %v", err)
		}
		if c != nil {
			t.Fatal("expected a nil client so the default behavior is kept")
		}
	})

	t.Run("CA file sets RootCAs and keeps HTTP/2", func(t *testing.T) {
		c, err := backendTLSClient(caPath, "", "")
		if err != nil {
			t.Fatalf("backendTLSClient: %v", err)
		}
		transport, ok := c.(*http.Client).Transport.(*http.Transport)
		if !ok {
			t.Fatalf("unexpected transport type %T", c.(*http.Client).Transport)
		}
		// RootCAs verifies the backend. ClientCAs would have no effect here.
		if transport.TLSClientConfig.RootCAs == nil {
			t.Error("RootCAs is not set, so the backend certificate is not verified")
		}
		// A custom TLSClientConfig disables the automatic HTTP/2 upgrade, which
		// gRPC needs.
		if !transport.ForceAttemptHTTP2 {
			t.Error("ForceAttemptHTTP2 is not set, so gRPC over TLS would fail")
		}
	})

	t.Run("client certificate loads", func(t *testing.T) {
		c, err := backendTLSClient("", certPath, keyPath)
		if err != nil {
			t.Fatalf("backendTLSClient: %v", err)
		}
		transport := c.(*http.Client).Transport.(*http.Transport)
		if len(transport.TLSClientConfig.Certificates) != 1 {
			t.Error("expected one client certificate")
		}
	})

	t.Run("certificate without a key is an error", func(t *testing.T) {
		if _, err := backendTLSClient("", certPath, ""); err == nil {
			t.Fatal("expected an error when the key is missing")
		}
	})

	t.Run("does not mutate http.DefaultClient", func(t *testing.T) {
		before := http.DefaultClient.Transport
		if _, err := backendTLSClient(caPath, "", ""); err != nil {
			t.Fatalf("backendTLSClient: %v", err)
		}
		if http.DefaultClient.Transport != before {
			t.Fatal("http.DefaultClient was mutated")
		}
	})

	t.Run("missing CA file is an error", func(t *testing.T) {
		if _, err := backendTLSClient(filepath.Join(t.TempDir(), "absent.pem"), "", ""); err == nil {
			t.Fatal("expected an error for a missing CA file")
		}
	})
}
