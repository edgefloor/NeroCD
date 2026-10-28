package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestStoredDockerfileRealDecoderBounds(t *testing.T) {
	settings := domain.HostingBuildSettings{Strategy: "dockerfile"}
	for i := range 64 {
		settings.BuildVariables = append(settings.BuildVariables, domain.HostingBuildVariable{Name: fmt.Sprintf("VAR_%d", i), Value: strings.Repeat("\x01", 4096)})
	}
	raw, err := json.Marshal(map[string]any{"source": map[string]any{"kind": "dockerfile", "dockerfile": map[string]any{"content": strings.Repeat("\x01", domain.HostingDockerfileContentLimit), "settings": settings}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= maxJSONBodyBytes || len(raw) > domain.HostingDockerfileResourceJSONLimit {
		t.Fatal("fixture does not exercise escaped source/settings bound", len(raw))
	}
	for _, path := range []string{"/api/v1/resources", "/api/v1/resources/res_one"} {
		method := http.MethodPost
		if path != "/api/v1/resources" {
			method = http.MethodPatch
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if !strictJSONDocument(rec, req) {
			t.Fatal("boundary rejected valid escaped maximum", rec.Code)
		}
		var decoded struct {
			Source domain.ResourceSourceInput `json:"source"`
		}
		if !decodeBody(rec, req, &decoded) || domain.ValidateResourceSource(decoded.Source.Source()) != nil {
			t.Fatal("typed decoder rejected maximum", rec.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/projects", "/api/v1/resources/res_one/builds"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if strictJSONDocument(rec, req) || rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatal("unrelated route limit increased", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/resources", strings.NewReader(strings.Repeat(" ", domain.HostingDockerfileResourceJSONLimit+1)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if strictJSONDocument(rec, req) || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("resource bound missing")
	}
}
