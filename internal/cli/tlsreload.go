package cli

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// certStore serves the certificate through GetCertificate so a reload swaps
// it for new handshakes without restarting the listener.
type certStore struct {
	certFile, keyFile string
	cur               atomic.Pointer[tls.Certificate]
}

func loadCert(certFile, keyFile string) (*certStore, error) {
	c := &certStore{certFile: certFile, keyFile: keyFile}
	return c, c.reload()
}

// reload keeps the current certificate when the files cannot be loaded.
func (c *certStore) reload() error {
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("load tls certificate: %w", err)
	}
	c.cur.Store(&cert)
	return nil
}

func (c *certStore) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.cur.Load(), nil
}

// notAfter reads Leaf, which LoadX509KeyPair fills since Go 1.23.
func (c *certStore) notAfter() time.Time { return c.cur.Load().Leaf.NotAfter }

// checkFunc reports err, labelled with what failed, when it is not nil.
type checkFunc func(ctx context.Context, what string, err error)

// watchHUP reloads the certificate on each signal until ctx ends.
func watchHUP(ctx context.Context, hup <-chan os.Signal, c *certStore, check checkFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			check(ctx, "reload tls certificate", c.reload())
		}
	}
}
