//go:build unix

package main

// A runner workspace has the same ownership and traversal threat model as a
// backup root: journals, checkouts, and prepared secret files must not be
// redirected through a writable parent or read by another local account.
func secureRunnerWorkspace(path string) error {
	return secureBackupDirectory(path)
}
