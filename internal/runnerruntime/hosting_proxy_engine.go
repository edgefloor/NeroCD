//go:build !nerocd_proxy_fixture

package runnerruntime

import "nerocd/internal/hostingproxy"

func newProxyEngine(settings hostingproxy.Settings) (managedProxyEngine, error) {
	return hostingproxy.New(settings)
}
