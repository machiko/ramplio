package distributed

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/machiko/ramplio/v3/internal/protocols"
	"github.com/machiko/ramplio/v3/internal/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// genSelfSignedCert writes a self-signed cert/key valid for 127.0.0.1 to temp
// files and returns their paths.
func genSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ramplio-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certOut, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certOut.Close())

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	keyOut, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyOut.Close())

	return certPath, keyPath
}

func tlsTestScenario(targetURL string) string {
	return fmt.Sprintf(`name: tls
stages:
  - duration: 1s
    target: 8
steps:
  - name: GET /
    method: GET
    url: %s/
    assertions:
      status: 200
`, targetURL)
}

// startTLSWorker boots a TLS worker on a free port and returns its https address.
func startTLSWorker(t *testing.T) string {
	t.Helper()
	cert, key := genSelfSignedCert(t)
	w := NewWorker("tls-worker")
	w.SetTLS(cert, key)
	port := findFreePort()
	go func() { _ = w.StartHTTPServer(":" + port) }()
	time.Sleep(200 * time.Millisecond)
	return "https://127.0.0.1:" + port
}

// TestDistributedTLSEndToEnd proves a coordinator can drive an HTTPS worker
// end-to-end when its client is configured to trust the self-signed cert.
func TestDistributedTLSEndToEnd(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		rw.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	addr := startTLSWorker(t)
	yaml := tlsTestScenario(target.URL)
	scParsed, perr := scenarios.Parse(bytes.NewReader([]byte(yaml)))
	require.NoError(t, perr)

	coord := NewCoordinator([]string{addr}, []byte(yaml), scParsed, protocols.DefaultHTTPConfig())
	coord.SetHTTPClient(&http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test cert
	})
	coord.SetTiming(200*time.Millisecond, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sum, err := coord.Run(ctx)
	require.NoError(t, err)
	assert.Greater(t, sum.Total, int64(0), "TLS distributed run must generate load")
	assert.Greater(t, hits.Load(), int64(0), "target should receive requests over the TLS worker")
}

// TestDistributedTLSRejectsUntrusted verifies the coordinator refuses to talk
// to an HTTPS worker when it cannot verify the cert (no skip-verify, no CA).
func TestDistributedTLSRejectsUntrusted(t *testing.T) {
	addr := startTLSWorker(t)
	yaml := tlsTestScenario("http://example.invalid")
	scParsed, perr := scenarios.Parse(bytes.NewReader([]byte(yaml)))
	require.NoError(t, perr)

	coord := NewCoordinator([]string{addr}, []byte(yaml), scParsed, protocols.DefaultHTTPConfig())
	// Default client does not trust the self-signed worker cert.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := coord.Run(ctx)
	require.Error(t, err, "untrusted TLS worker must fail the health check")
	assert.Contains(t, err.Error(), "health check")
}
