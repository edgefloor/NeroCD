package runnerclient

import (
	"encoding/json"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStorageRetentionIdentityRequiresSeparateOptInAndNoDatabase(t *testing.T) {
	base := []string{domain.HostingOwnedStorageCapability, domain.RunTypeStorageArchive, domain.HostingStorageArchiveCapability}
	for _, tc := range []struct {
		name                                 string
		configured, advertised, shell, valid bool
	}{{"archive only", false, false, false, true}, {"configured old agent", true, false, false, false}, {"unconfigured advertised", false, true, false, false}, {"storage only", true, true, false, true}, {"shell rejected", true, true, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			caps := append([]string(nil), base...)
			if tc.advertised {
				caps = append(caps, domain.RunTypeStorageBackupRetention, domain.HostingStorageBackupRetentionCapability)
			}
			if tc.shell {
				caps = append(caps, "shell")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps})
			}))
			defer server.Close()
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), RuntimeExecutionOptions{StorageArchive: true, StorageArchiveTrustedLoopbackHTTP: true, StorageBackupRetention: tc.configured})
			if (err == nil) != tc.valid {
				t.Fatal("identity opt-in", err)
			}
		})
	}
}
