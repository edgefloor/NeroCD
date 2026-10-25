//go:build !unix

package runnerruntime

import (
	"errors"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runnerclient"
)

func applyApplicationStorageProxy(_ *attemptSupervisor, _ runnerclient.Client, _ managedProxyEngine, local hostingproxy.Plan, op domain.HostingProxyOperation, _ domain.HostingAuthority, _ ProxyConfig) (hostingproxy.Result, error) {
	return hostingproxy.Result{Outcome: hostingproxy.Refused, DesiredGeneration: uint64(op.Generation), Hash: local.Hash()}, errors.New("connected storage proxy execution requires native Linux")
}
