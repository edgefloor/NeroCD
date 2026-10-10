//go:build !unix

package runnerruntime

import "os/exec"

func configureProvenanceProcess(command *exec.Cmd) {}
func killProvenanceProcess(command *exec.Cmd)      {}
