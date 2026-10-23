package domain

import "time"

// ServerBootstrapProbeReceipt is a closed snapshot committed under live stage authority.
// Observation and expiry times are assigned by the database at completion.
type ServerBootstrapProbeReceipt struct {
	Prerequisites ServerBootstrapPrerequisites `json:"prerequisites"`
	ObservedAt    time.Time                    `json:"observed_at"`
	ExpiresAt     time.Time                    `json:"expires_at"`
}

// ServerBootstrapOwnedReceipt binds an installer response to exact durable intent.
// Installed and quiescent state alone cannot establish ownership.
type ServerBootstrapOwnedReceipt struct {
	InstallationID       string `json:"installation_id"`
	ManifestSHA256       string `json:"manifest_sha256"`
	OperationID          string `json:"operation_id"`
	State                string `json:"state"`
	ArtifactMatches      bool   `json:"artifact_matches"`
	ServiceActive        bool   `json:"service_active"`
	ServiceJobPending    bool   `json:"service_job_pending"`
	EnrollmentFileExists bool   `json:"enrollment_file_exists"`
	CredentialFileExists bool   `json:"credential_file_exists"`
}
