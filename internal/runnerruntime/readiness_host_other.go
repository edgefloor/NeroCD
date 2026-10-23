//go:build !linux

package runnerruntime

// This profile has no qualified filesystem implementation outside Linux.
func readinessCLI() string                       { return "unknown" }
func readinessRoot(string, bool) (string, int64) { return "unknown", 0 }
