package domain

// HostingSourceGrantRequest selects one immutable secret version for current source intent.
// RequestID must be a cryptographically random canonical delivery-style identifier.
type HostingSourceGrantRequest struct {
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
	ExpectedSourceGeneration   int64  `json:"expected_source_generation"`
	SecretID                   string `json:"secret_id"`
	SecretVersion              int64  `json:"secret_version"`
	RequestID                  string `json:"request_id"`
}

// PublicHostingSourceGrant exposes bounded selection metadata without secret references.
type PublicHostingSourceGrant struct {
	ID               string `json:"id"`
	Version          int64  `json:"version"`
	SourceID         string `json:"source_id"`
	SourceGeneration int64  `json:"source_generation"`
	ResourceID       string `json:"resource_id"`
	Kind             string `json:"kind"`
	Revoked          bool   `json:"revoked"`
}

// HostingSourceGrantID binds retry identity to one resource without caller-chosen grants.
func HostingSourceGrantID(resource, request string) string {
	return "source-grant-" + HostingDocumentHash(struct{ Resource, Request string }{resource, request})[7:]
}
