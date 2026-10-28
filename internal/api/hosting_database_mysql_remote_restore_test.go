package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestMySQLRemoteRestoreRealDecoder(t *testing.T) {
	body := `{"request_id":"mysql_restore","name":"Separate MySQL target","remote_source":{"transfer_id":"mysql_upload","expected_target_revision":1}}`
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"valid", body, true},
		{"plaintext", strings.Replace(body, `"name":`, `"password":"canary","name":`, 1), false},
		{"engine", strings.Replace(body, `"name":`, `"engine":"postgresql","name":`, 1), false},
		{"duplicate", strings.Replace(body, `"transfer_id":"mysql_upload"`, `"transfer_id":"mysql_upload","transfer_id":"other"`, 1), false},
		{"invalid_id", strings.Replace(body, "mysql_upload", "", 1), false},
		{"revision", strings.Replace(body, `"expected_target_revision":1`, `"expected_target_revision":0`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input domain.HostingDatabaseBackupRestoreInput
			ok := decodeDatabaseBackup(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(tc.body)), &input)
			if ok {
				ok = input.Validate() == nil
			}
			if ok != tc.valid {
				t.Fatal("restore decoder contract mismatch")
			}
		})
	}
}
