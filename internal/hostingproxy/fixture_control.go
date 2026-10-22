//go:build nerocd_proxy_fixture

package hostingproxy

import (
	"crypto/x509"
	"errors"
)

// NewFixture is available only in explicitly tagged disposable acceptance builds.
// Production binaries have no custom CA, root-trust or Docker DNS override API.
func NewFixture(settings Settings, caURL string, roots *x509.CertPool, dockerArgs []string) (*Engine, error) {
	if caURL == "" || roots == nil {
		return nil, errors.New("fixture CA and trust roots are required")
	}
	engine, err := New(settings)
	if err != nil {
		return nil, err
	}
	engine.fixtureCA = caURL
	engine.fixtureRoots = roots.Clone()
	engine.fixtureDockerArgs = append([]string(nil), dockerArgs...)
	return engine, nil
}
