//go:build nerocd_proxy_fixture

package hostingfixture

import (
	"strings"
	"testing"
)

func TestGitHTTPSMutableAuthenticatedSource(t *testing.T) {
	url, update, unavailable := GitHTTPS(t, map[string]string{"project/Dockerfile": "FROM scratch\n"}, "fixture-user", "fixture-password")
	if !strings.HasPrefix(url, "https://127.0.0.1:") || !strings.HasSuffix(url, "/repo.git") {
		t.Fatal("fixture Git URL changed")
	}
	first := update(nil)
	second := update(map[string]string{"project/Dockerfile": "FROM scratch\nLABEL version=B\n"})
	if len(first) != 40 || len(second) != 40 || first == second || update(nil) != second {
		t.Fatal("fixture did not publish an exact changed Git revision")
	}
	unavailable(true)
	unavailable(false)
}
