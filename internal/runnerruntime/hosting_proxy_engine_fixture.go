//go:build nerocd_proxy_fixture

package runnerruntime

import (
	"crypto/x509"
	"errors"
	"nerocd/internal/hostingproxy"
	"os"
)

func newProxyEngine(settings hostingproxy.Settings) (managedProxyEngine, error) {
	if os.Getenv("NEROCD_PROXY_COMPILED_FIXTURE") != "disposable-dind" {
		return hostingproxy.New(settings)
	}
	root, err := os.ReadFile(os.Getenv("NEROCD_PROXY_FIXTURE_ROOT"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return nil, errors.New("invalid compiled proxy fixture roots")
	}
	return hostingproxy.NewFixture(settings, "https://localhost:14000/dir", roots, []string{"--env", "LEGO_CA_CERTIFICATES=/certificates/root.pem"})
}
