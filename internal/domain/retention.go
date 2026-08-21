package domain

import "time"

// RunLogRetentionPolicy is the single global, manual retention control.  A
// disabled policy never deletes data; callers must explicitly execute a
// previewed policy with a stable request ID.
type RunLogRetentionPolicy struct {
	Enabled   bool      `json:"enabled"`
	KeepDays  int       `json:"keep_days"`
	BatchSize int       `json:"batch_size"`
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RunLogRetentionPreview struct {
	Cutoff        time.Time `json:"cutoff"`
	EligibleLogs  int64     `json:"eligible_logs"`
	EligibleBytes int64     `json:"eligible_bytes"`
}

type RunLogRetentionExecution struct {
	RequestID    string                 `json:"request_id"`
	Policy       RunLogRetentionPolicy  `json:"policy"`
	Preview      RunLogRetentionPreview `json:"preview"`
	Deleted      int64                  `json:"deleted"`
	DeletedBytes int64                  `json:"deleted_bytes"`
}
