package domain

import "time"

// HostingBuildRequest admits intent only; policy, credentials and placement are server owned.
type HostingBuildRequest struct {
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
	ExpectedSourceGeneration   int64  `json:"expected_source_generation"`
	IdempotencyKey             string `json:"idempotency_key"`
	GrantID                    string `json:"grant_id,omitempty"`
}

// PublicHostingBuild is a deliberate whitelist, never the runner snapshot.
type PublicHostingBuild struct {
	ID            string                           `json:"id"`
	ProjectID     string                           `json:"project_id"`
	ResourceID    string                           `json:"resource_id"`
	State         string                           `json:"state"`
	Revision      int64                            `json:"revision"`
	RunID         string                           `json:"run_id,omitempty"`
	CreatedBy     string                           `json:"created_by"`
	CreatedAt     time.Time                        `json:"created_at"`
	UpdatedAt     time.Time                        `json:"updated_at"`
	FailureCode   string                           `json:"failure_code,omitempty"`
	Snapshot      PublicHostingBuildSnapshot       `json:"snapshot"`
	SourceReceipt *PublicHostingBuildSourceReceipt `json:"source_receipt,omitempty"`
	Artifact      *PublicHostingBuildArtifact      `json:"artifact,omitempty"`
	ComposeResult *PublicHostingBuildComposeResult `json:"compose_result,omitempty"`
}

// PublicHostingBuildSnapshot exposes only source intent and exact placement.
type PublicHostingBuildSnapshot struct {
	ResourceGeneration int64                        `json:"resource_generation"`
	Source             PublicHostingBuildSource     `json:"source"`
	Target             PublicHostingBuildTarget     `json:"target"`
	TriggerRevision    *HostingBuildTriggerRevision `json:"trigger_revision,omitempty"`
}

// PublicHostingBuildSource excludes source transport policy and grants.
type PublicHostingBuildSource struct {
	SourceID       string               `json:"source_id"`
	Generation     int64                `json:"generation"`
	RepositoryURL  string               `json:"repository_url"`
	RequestedRef   string               `json:"requested_ref"`
	CredentialKind string               `json:"credential_kind"`
	Settings       HostingBuildSettings `json:"settings"`
}

// PublicHostingBuildTarget describes fixed desired execution placement.
type PublicHostingBuildTarget struct {
	BindingID     string `json:"binding_id"`
	DestinationID string `json:"destination_id"`
	ServerID      string `json:"server_id"`
	RunnerID      string `json:"runner_id"`
}

// PublicHostingBuildSourceReceipt omits internal policy/input fingerprints.
type PublicHostingBuildSourceReceipt struct {
	Commit     string `json:"commit"`
	TreeDigest string `json:"tree_digest"`
}

// PublicHostingBuildArtifact exposes content identity without internal policy or request hashes.
type PublicHostingBuildArtifact struct {
	Origin         string `json:"origin"`
	ImageID        string `json:"image_id"`
	IndexDigest    string `json:"index_digest"`
	ManifestDigest string `json:"manifest_digest"`
	ConfigDigest   string `json:"config_digest"`
	Platform       string `json:"platform"`
}

// PublicBuild projects retained metadata without serializing a trusted snapshot.
func PublicBuild(b HostingBuild) PublicHostingBuild {
	s := b.Snapshot
	p := PublicHostingBuild{ID: b.ID, ProjectID: b.ProjectID, ResourceID: b.ResourceID, State: b.State, Revision: b.Revision, RunID: b.RunID, CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt, FailureCode: b.FailureCode, Snapshot: PublicHostingBuildSnapshot{ResourceGeneration: s.ResourceGeneration, Source: PublicHostingBuildSource{SourceID: s.Source.ID, Generation: s.Source.Generation, RepositoryURL: s.Source.RepositoryURL, RequestedRef: s.Source.RequestedRef, CredentialKind: s.Source.CredentialKind, Settings: s.Source.Settings}, Target: PublicHostingBuildTarget{BindingID: s.BindingID, DestinationID: s.DestinationID, ServerID: s.ServerID, RunnerID: s.RunnerID}}}
	if s.TriggerRevision != nil {
		revision := *s.TriggerRevision
		p.Snapshot.TriggerRevision = &revision
	}
	if b.SourceReceipt != nil {
		p.SourceReceipt = &PublicHostingBuildSourceReceipt{Commit: b.SourceReceipt.Commit, TreeDigest: b.SourceReceipt.TreeDigest}
	}
	if a := b.Artifact; a != nil {
		p.Artifact = &PublicHostingBuildArtifact{Origin: a.Origin, ImageID: a.ImageID, IndexDigest: a.IndexDigest, ManifestDigest: a.ManifestDigest, ConfigDigest: a.ConfigDigest, Platform: a.Platform}
	}
	if c := b.ComposeResult; c != nil {
		p.ComposeResult = &PublicHostingBuildComposeResult{Source: c.Source, Definition: c.Definition, Services: []PublicHostingBuildComposeService{}, Hash: c.Hash}
		for _, service := range c.Services {
			a := service.Artifact
			p.ComposeResult.Services = append(p.ComposeResult.Services, PublicHostingBuildComposeService{Component: service.Component, Artifact: PublicHostingBuildArtifact{Origin: a.Origin, ImageID: a.ImageID, IndexDigest: a.IndexDigest, ManifestDigest: a.ManifestDigest, ConfigDigest: a.ConfigDigest, Platform: a.Platform}})
		}
	}
	return p
}

// PublicHostingBuildComposeResult exposes prepared source and component image identities without transport policy.
type PublicHostingBuildComposeResult struct {
	Source     HostingStackSource                 `json:"source"`
	Definition HostingStackDefinition             `json:"definition"`
	Services   []PublicHostingBuildComposeService `json:"services"`
	Hash       string                             `json:"hash"`
}

// PublicHostingBuildComposeService identifies a built component without internal request fingerprints.
type PublicHostingBuildComposeService struct {
	Component string                     `json:"component"`
	Artifact  PublicHostingBuildArtifact `json:"artifact"`
}
