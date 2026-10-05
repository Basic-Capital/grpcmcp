package main

import (
	"bytes"
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
	"strings"
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

// TestServerTLSConfigRequiresClientCertificate covers the inbound mTLS wiring.
// ClientCAs is the server-side field and ClientAuth is what makes the server ask
// for a certificate, so a mix-up with RootCAs, or a missing ClientAuth, would
// accept a client that presents nothing.
func TestServerTLSConfigRequiresClientCertificate(t *testing.T) {
	certPath, keyPath, caPath := writeSelfSignedCert(t)

	cfg, err := serverTLSConfig(caPath)
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("ClientCAs is not set, so inbound certificates are not verified")
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}

	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	cfg.Certificates = []tls.Certificate{serverCert}

	// Port 0 lets the kernel pick, so nothing races for the address.
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	listener := tls.NewListener(rawListener, cfg)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { srv.Close() })

	url := "https://" + rawListener.Addr().String() + "/"
	pool, err := certPoolFromFile(caPath)
	if err != nil {
		t.Fatalf("certPoolFromFile: %v", err)
	}

	t.Run("a client with no certificate is rejected", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			t.Fatal("the server accepted a client that presented no certificate")
		}
	})

	t.Run("a client with a certificate from the CA succeeds", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{serverCert},
			},
		}}
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("a client with a valid certificate was rejected: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// No caFile means no client certificate is demanded.
func TestServerTLSConfigWithoutCAFileDoesNotRequireClientCert(t *testing.T) {
	cfg, err := serverTLSConfig("")
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	if cfg.ClientCAs != nil {
		t.Error("ClientCAs is set without a CA file")
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Errorf("ClientAuth = %v, want NoClientCert", cfg.ClientAuth)
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

	// The TLS client replaces http.DefaultClient on the backend path, so it must
	// keep what DefaultTransport provides. A transport built from an empty literal
	// silently drops all of it.
	t.Run("inherits the DefaultTransport settings", func(t *testing.T) {
		c, err := backendTLSClient(caPath, "", "")
		if err != nil {
			t.Fatalf("backendTLSClient: %v", err)
		}
		got := c.(*http.Client).Transport.(*http.Transport)
		want := http.DefaultTransport.(*http.Transport)

		// A nil Proxy means no proxying at all, so HTTPS_PROXY and NO_PROXY stop
		// working. It is not the same as ProxyFromEnvironment.
		if got.Proxy == nil {
			t.Error("Proxy is nil, so HTTP_PROXY, HTTPS_PROXY, and NO_PROXY are ignored")
		}
		// An unset IdleConnTimeout means no limit, which keeps an idle connection
		// and its goroutine for the life of the process.
		if got.IdleConnTimeout != want.IdleConnTimeout {
			t.Errorf("IdleConnTimeout = %v, want %v", got.IdleConnTimeout, want.IdleConnTimeout)
		}
		if got.TLSHandshakeTimeout != want.TLSHandshakeTimeout {
			t.Errorf("TLSHandshakeTimeout = %v, want %v", got.TLSHandshakeTimeout, want.TLSHandshakeTimeout)
		}
		if got.MaxIdleConns != want.MaxIdleConns {
			t.Errorf("MaxIdleConns = %v, want %v", got.MaxIdleConns, want.MaxIdleConns)
		}
		if got.ExpectContinueTimeout != want.ExpectContinueTimeout {
			t.Errorf("ExpectContinueTimeout = %v, want %v", got.ExpectContinueTimeout, want.ExpectContinueTimeout)
		}
		if got.DialContext == nil {
			t.Error("DialContext is nil, so the dial has no timeout")
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
		if transport.TLSClientConfig.GetClientCertificate == nil {
			t.Fatal("GetClientCertificate is not set, so no client certificate is sent")
		}
		cert, err := transport.TLSClientConfig.GetClientCertificate(&tls.CertificateRequestInfo{})
		if err != nil || cert == nil {
			t.Fatalf("GetClientCertificate = %v, %v; want a certificate", cert, err)
		}
	})

	t.Run("bad key path is an error at startup", func(t *testing.T) {
		if _, err := backendTLSClient("", certPath, filepath.Join(t.TempDir(), "absent.pem")); err == nil {
			t.Fatal("expected an error for a missing key file")
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

func copyFile(t *testing.T, src string, dst string, mod time.Time) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	if err := os.Chtimes(dst, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", dst, err)
	}
}

func TestCertReloaderPicksUpRotatedCertificate(t *testing.T) {
	oldCert, oldKey, _ := writeSelfSignedCert(t)
	newCert, newKey, _ := writeSelfSignedCert(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	start := time.Now().Add(-time.Hour)
	copyFile(t, oldCert, certPath, start)
	copyFile(t, oldKey, keyPath, start)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	before, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}

	rotated := start.Add(time.Minute)
	copyFile(t, newCert, certPath, rotated)
	copyFile(t, newKey, keyPath, rotated)

	after, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate after rotation: %v", err)
	}
	want, err := tls.LoadX509KeyPair(newCert, newKey)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if string(after.Certificate[0]) == string(before.Certificate[0]) {
		t.Fatal("the reloader kept the old certificate after the files changed")
	}
	if string(after.Certificate[0]) != string(want.Certificate[0]) {
		t.Fatal("the reloader did not return the rotated certificate")
	}
}

func TestCertReloaderKeepsLastCertificateWhenFilesDisappear(t *testing.T) {
	srcCert, srcKey, _ := writeSelfSignedCert(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	now := time.Now()
	copyFile(t, srcCert, certPath, now)
	copyFile(t, srcKey, keyPath, now)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	before, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}

	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	after, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate with the file absent: %v", err)
	}
	if string(after.Certificate[0]) != string(before.Certificate[0]) {
		t.Fatal("expected the previous certificate while the file is absent")
	}
}

func TestCertReloaderLogsARepeatedFailureOnce(t *testing.T) {
	srcCert, srcKey, _ := writeSelfSignedCert(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	start := time.Now().Add(-time.Hour)
	copyFile(t, srcCert, certPath, start)
	copyFile(t, srcKey, keyPath, start)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	var log bytes.Buffer
	r.log = &log
	lines := func() int { return strings.Count(log.String(), "\n") }

	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for range 3 {
		if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
			t.Fatalf("GetCertificate: %v", err)
		}
	}
	if lines() != 1 {
		t.Fatalf("logged %d lines for one sustained failure, want 1:\n%s", lines(), log.String())
	}

	copyFile(t, srcCert, certPath, start.Add(time.Minute))
	if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate after recovery: %v", err)
	}
	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := r.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if lines() != 2 {
		t.Fatalf("logged %d lines, want 2 after a recovery and a new failure:\n%s", lines(), log.String())
	}
}

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.entered <- struct{}{}
	<-w.release
	return len(p), nil
}

func TestCertReloaderDoesNotHoldTheLockWhileItLogs(t *testing.T) {
	oldCert, oldKey, _ := writeSelfSignedCert(t)
	newCert, newKey, _ := writeSelfSignedCert(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	start := time.Now().Add(-time.Hour)
	copyFile(t, oldCert, certPath, start)
	copyFile(t, oldKey, keyPath, start)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	r.log = w
	defer close(w.release)

	rotated := start.Add(time.Minute)
	copyFile(t, newCert, certPath, rotated)
	copyFile(t, newKey, keyPath, rotated)

	go func() { _, _ = r.GetCertificate(&tls.ClientHelloInfo{}) }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the rotation did not write a log line")
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GetClientCertificate: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a handshake waited for a blocked log write")
	}
}

func TestCertReloaderDoesNotFallBackToAnExpiredCertificate(t *testing.T) {
	srcCert, srcKey, _ := writeSelfSignedCert(t)
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	now := time.Now()
	copyFile(t, srcCert, certPath, now)
	copyFile(t, srcKey, keyPath, now)

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	r.now = func() time.Time { return now.Add(2 * time.Hour) }
	var log bytes.Buffer
	r.log = &log

	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cert, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err == nil {
		t.Fatalf("GetClientCertificate = %v; want an error, not an expired certificate", cert)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("the handshake error %q contains the certificate path, which a caller can see", err)
	}
	if !strings.Contains(log.String(), certPath) || !strings.Contains(log.String(), "expired") {
		t.Errorf("the log %q does not give the path and the expiry", log.String())
	}
}

func TestLoadKeyPairSetsLeafWhenGODEBUGDisablesIt(t *testing.T) {
	certPath, keyPath, _ := writeSelfSignedCert(t)

	plain, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if plain.Leaf != nil {
		t.Skip("LoadX509KeyPair set Leaf; run with GODEBUG=x509keypairleaf=0 at process start on Go 1.23 to 1.26 to test a nil Leaf")
	}

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	cert, err := r.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("Leaf is nil, so the expiry checks would panic")
	}
}

func TestServeTLSPicksUpRotatedCertificate(t *testing.T) {
	oldCert, oldKey, oldCA := writeSelfSignedCert(t)
	newCert, newKey, newCA := writeSelfSignedCert(t)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")
	start := time.Now().Add(-time.Hour)
	copyFile(t, oldCert, certPath, start)
	copyFile(t, oldKey, keyPath, start)

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

	pool := x509.NewCertPool()
	for _, path := range []string{oldCA, newCA} {
		pems, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		pool.AppendCertsFromPEM(pems)
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool},
		DisableKeepAlives: true,
	}}

	served := func() []byte {
		t.Helper()
		var resp *http.Response
		var err error
		for range 50 {
			resp, err = client.Get("https://" + addr + "/")
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("HTTPS request failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.TLS.PeerCertificates[0].Raw
	}

	want, err := tls.LoadX509KeyPair(oldCert, oldKey)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if string(served()) != string(want.Certificate[0]) {
		t.Fatal("the server did not serve the initial certificate")
	}

	rotated := start.Add(time.Minute)
	copyFile(t, newCert, certPath, rotated)
	copyFile(t, newKey, keyPath, rotated)

	want, err = tls.LoadX509KeyPair(newCert, newKey)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if string(served()) != string(want.Certificate[0]) {
		t.Fatal("the server kept the old certificate after the files changed")
	}
}

func TestCertReloaderFollowsKubernetesSecretVolumeSwap(t *testing.T) {
	oldCert, oldKey, _ := writeSelfSignedCert(t)
	newCert, newKey, _ := writeSelfSignedCert(t)

	mount := t.TempDir()
	writeVersion := func(name string, cert string, key string, mod time.Time) {
		t.Helper()
		dir := filepath.Join(mount, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		copyFile(t, cert, filepath.Join(dir, "tls.crt"), mod)
		copyFile(t, key, filepath.Join(dir, "tls.key"), mod)
	}
	swapData := func(target string) {
		t.Helper()
		tmp := filepath.Join(mount, "..data_tmp")
		if err := os.Symlink(target, tmp); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := os.Rename(tmp, filepath.Join(mount, "..data")); err != nil {
			t.Fatalf("rename: %v", err)
		}
	}

	writeVersion("..2026_10_02_19_13_21.1", oldCert, oldKey, time.Now().Add(-time.Hour))
	swapData("..2026_10_02_19_13_21.1")
	for _, name := range []string{"tls.crt", "tls.key"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(mount, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	certPath := filepath.Join(mount, "tls.crt")
	keyPath := filepath.Join(mount, "tls.key")

	r, err := newCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	before, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}

	writeVersion("..2026_10_08_03_12_31.2", newCert, newKey, time.Now())
	swapData("..2026_10_08_03_12_31.2")
	if err := os.RemoveAll(filepath.Join(mount, "..2026_10_02_19_13_21.1")); err != nil {
		t.Fatalf("remove old version: %v", err)
	}

	after, err := r.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate after the swap: %v", err)
	}
	want, err := tls.LoadX509KeyPair(newCert, newKey)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if string(after.Certificate[0]) == string(before.Certificate[0]) {
		t.Fatal("the reloader kept the old certificate after the ..data symlink swap")
	}
	if string(after.Certificate[0]) != string(want.Certificate[0]) {
		t.Fatal("the reloader did not return the certificate from the new version directory")
	}
}
