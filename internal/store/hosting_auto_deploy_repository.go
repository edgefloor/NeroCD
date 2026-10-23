package store

import "context"

// HostingAutoDeployRepository advances only persisted reviewed demand. The
// opaque wakeup never carries source, credentials, plans or execution authority.
type HostingAutoDeployRepository interface {
	PrepareHostingAutoDeploy(context.Context, string) error
}
