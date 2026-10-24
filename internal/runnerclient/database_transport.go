package runnerclient

import "context"

// DatabaseTransport requires verified controller transport for database work.
// TrustedLoopbackHTTP permits literal-loopback fixtures only; CLI callers omit it.
type DatabaseTransport struct{ TrustedLoopbackHTTP bool }

type databaseTransportKey struct{}

// WithDatabaseTransport carries an explicit request transport policy through
// attempt cancellation, renewal, reporting, and journal replay contexts.
// A child cannot weaken a parent's HTTPS requirement to permit fixture HTTP.
func WithDatabaseTransport(parent context.Context, policy DatabaseTransport) context.Context {
	if previous, ok := parent.Value(databaseTransportKey{}).(DatabaseTransport); ok && !previous.TrustedLoopbackHTTP {
		policy.TrustedLoopbackHTTP = false
	}
	return context.WithValue(parent, databaseTransportKey{}, policy)
}
