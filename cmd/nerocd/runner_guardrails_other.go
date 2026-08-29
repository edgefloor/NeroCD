//go:build !unix

package main

import "errors"

func secureRunnerWorkspace(string) error {
	return errors.New("secure runner workspaces are unsupported on this platform")
}
