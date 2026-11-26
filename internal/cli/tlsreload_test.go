package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// writeCert writes a self-signed certificate for localhost and its key, and
// returns the certificate.
func writeCert(t *testing.T, certFile, keyFile string, serial int64, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func servedSerial(t *testing.T, c *certStore) int64 {
	t.Helper()
	got, err := c.getCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return got.Leaf.SerialNumber.Int64()
}

func TestLoadCertFailsWithoutFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadCert(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")); err == nil {
		t.Fatal("loadCert with missing files succeeded")
	}
}

func TestSIGHUPReloadsCertificateAndKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	notAfter := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	writeCert(t, certFile, keyFile, 1, notAfter)
	c, err := loadCert(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if servedSerial(t, c) != 1 || !c.notAfter().Equal(notAfter) {
		t.Fatalf("serial, notAfter = %d, %v; want 1, %v", servedSerial(t, c), c.notAfter(), notAfter)
	}

	hup := make(chan os.Signal)
	results := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchHUP(ctx, hup, c, func(_ context.Context, _ string, err error) { results <- err })
		close(done)
	}()

	writeCert(t, certFile, keyFile, 2, notAfter)
	hup <- syscall.SIGHUP
	if err := <-results; err != nil || servedSerial(t, c) != 2 {
		t.Fatalf("after SIGHUP: serial %d, err %v; want 2, nil", servedSerial(t, c), err)
	}

	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCert(t, certFile, filepath.Join(dir, "other.pem"), 3, notAfter)
	hup <- syscall.SIGHUP
	if err := <-results; err == nil || servedSerial(t, c) != 2 {
		t.Fatalf("after failed reload: serial %d, err %v; want 2 and an error", servedSerial(t, c), err)
	}

	cancel()
	<-done
}

func TestReportToReportsOnlyErrors(t *testing.T) {
	r := &recordingReporter{}
	check := reportTo(r)
	check(context.Background(), "step", nil)
	check(context.Background(), "step", errors.New("boom"))
	if len(r.errs) != 1 || r.errs[0].Error() != "step: boom" {
		t.Errorf("reported %v; want exactly [step: boom]", r.errs)
	}
}
