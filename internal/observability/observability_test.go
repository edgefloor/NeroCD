package observability

import (
	"strings"
	"testing"
)

func TestRenderUsesOnlyFixedLabelsAndRejectsInvalidData(t *testing.T) {
	snapshot := Snapshot{BackupOutcome: BackupSuccess, BackupReason: "none", TerminalRuns: map[string]DurationAggregate{"succeeded": {Count: 1, SumSeconds: 2}}, Deployments: map[string]int64{"succeeded": 1}}
	rendered, err := Render("nerocd_http_requests_total{method=\"GET\",path=\"/api/v1/runs\",status=\"2xx\"} 1\n", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"project", "runner_id", "token", "https://", "path=\"/tmp"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("rendered metrics exposed %q: %s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, `nerocd_backup_last_result{outcome="success"} 1`) || !strings.Contains(rendered, `nerocd_deployments{status="succeeded"} 1`) {
		t.Fatalf("missing fixed aggregate series: %s", rendered)
	}
	if _, err := Render("", Snapshot{BackupOutcome: "secret-value", BackupReason: "none"}); err == nil {
		t.Fatal("invalid enum accepted")
	}
}
