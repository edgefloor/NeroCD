package store

import "nerocd/internal/domain"

// MutationOption extends a repository mutation with a side effect that must
// commit atomically with the mutation itself.
type MutationOption func(*mutationOptions)

type mutationOptions struct {
	audit *domain.AuditEvent
}

// WithAudit records the event in the same transaction (PostgreSQL) or under
// the same lock (memory) as the mutation it is passed to. Passing more than
// one audit option to a single mutation is a programming error; the last one
// wins.
func WithAudit(event domain.AuditEvent) MutationOption {
	return func(options *mutationOptions) { options.audit = &event }
}

func resolveMutationOptions(opts []MutationOption) *domain.AuditEvent {
	options := &mutationOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}
	return options.audit
}
