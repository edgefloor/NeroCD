//go:build !unix

package main

import "os/exec"

func configureProvenanceProcess(command *exec.Cmd) {}
func killProvenanceProcess(command *exec.Cmd)      {}
