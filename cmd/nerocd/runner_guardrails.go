package main

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// validateRunnerOperatingGuardrails is intentionally separate from runner
// execution. Production workers execute privileged deployment plans, so their
// control-plane endpoint and durable workspace are admission requirements.
func validateRunnerOperatingGuardrails(server, workspace string) error {
	mode, err := configuredMode()
	if err != nil {
		return err
	}
	// A missing mode is never an implicit approval to run a privileged worker
	// over HTTP or from /tmp. Operators must explicitly select development mode
	// for local-only runner behavior.
	if mode == modeDevelopment && strings.EqualFold(strings.TrimSpace(os.Getenv("NEROCD_MODE")), string(modeDevelopment)) {
		return nil
	}
	endpoint, err := url.Parse(strings.TrimSpace(server))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return errors.New("production runner requires an HTTPS control-plane endpoint")
	}
	if err := secureRunnerWorkspace(workspace); err != nil {
		return errors.New("production runner requires an existing owner-only mode-0700 dedicated workspace")
	}
	return nil
}
