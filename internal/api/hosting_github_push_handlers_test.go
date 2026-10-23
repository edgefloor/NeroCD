package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func TestGitHubPushClosedOwnerCommands(t *testing.T) {
	configure := `{"expected_policy_generation":0,"expected_resource_generation":1,"expected_source_generation":1,"request_id":"00000000-0000-0000-0000-000000000001","reset_branch_baseline":true,"configuration":{"automation_actor_id":"bot","webhook_secret":{"id":"secret","version":1}}}`
	cases := []string{configure, strings.Replace(configure, `"webhook_secret":`, `"provider_read_secret":{"id":"provider","version":1},"webhook_secret":`, 1)}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		var value domain.GitHubPushConfigure
		if !decodeGitHubPushCommand(rec, req, &value) || value.Validate() != nil {
			t.Fatal("valid closed command rejected", rec.Body.String())
		}
	}
	for _, body := range []string{
		`null`, `{}`, configure + `{}`, strings.Replace(configure, `"reset_branch_baseline":true,`, "", 1), strings.Replace(configure, `"reset_branch_baseline":true`, `"reset_branch_baseline":null`, 1), strings.Replace(configure, `"version":1`, `"version":1,"version":2`, 1), strings.Replace(configure, `"version":1`, `"Version":1`, 1), strings.Replace(configure, `"version":1`, `"version":1,"url":"https://foreign.invalid"`, 1), strings.Replace(configure, `"version":1`, `"version":1.5`, 1), strings.Replace(configure, `"webhook_secret":{`, `"webhook_secret":null,"unused":{`, 1), strings.Replace(configure, `"id":"secret"`, `"id":"\ud800"`, 1), strings.Replace(configure, `"automation_actor_id":"bot"`, `"automation_actor_id":false`, 1),
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		var value domain.GitHubPushConfigure
		if decodeGitHubPushCommand(rec, req, &value) || rec.Code != 400 {
			t.Fatal("malformed command accepted", body, rec.Code)
		}
	}
	for _, body := range []string{`{"expected_policy_generation":1,"request_id":"x","enabled":null}`, `{"expected_policy_generation":1,"request_id":"x"}`, `{"expected_policy_generation":1,"request_id":"x","enabled":false,"Enabled":true}`} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var value domain.GitHubPushEnable
		if decodeGitHubPushCommand(rec, req, &value) {
			t.Fatal("missing/null boolean accepted")
		}
	}
}
func TestGitHubPushRegisteredAuthenticationAndRawWebhookBoundary(t *testing.T) {
	s, _ := newTestServer()
	session := createTestSession(t, s)
	for _, path := range []string{"/api/v1/resources/missing/github-push-policy/verify", "/api/v1/resources/missing/github-push-policy/enabled", "/api/v1/resources/missing/github-push-policy/retry"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal(path, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/resources/missing/github-push-policy", nil)
	req.Header.Set("Authorization", "Bearer "+session)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// Deliberately invalid JSON still reaches signature/current authority handling.
	// Global parsing before HMAC would instead return400.
	webhook := "/api/v1/hosting/github-push/ghp_fixture"
	call := func(body string, duplicate bool, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, webhook, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Delivery", "00000000-0000-0000-0000-000000000001")
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-Hub-Signature-256", "sha256:"+strings.Repeat("a", 64))
		if duplicate {
			req.Header.Add("X-GitHub-Event", "push")
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	call(`not JSON`, false, 503)
	call(`{}`, true, 400)
	call(strings.Repeat("x", domain.GitHubPushPayloadLimit+1), false, 413)
}
func TestGitHubPushResponseCallbackUsesConfiguredOrigin(t *testing.T) {
	s, _ := newTestServer()
	s.publicOrigin = "https://control.example"
	rec := httptest.NewRecorder()
	s.pushPolicyResponse(rec, domain.GitHubPushPolicy{ID: "ghp_fixture"}, nil)
	var p domain.GitHubPushPolicy
	if json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.CallbackURL != "https://control.example/api/v1/hosting/github-push/ghp_fixture" {
		t.Fatal(rec.Body.String())
	}
}

func TestGitHubPushPUTRejectsWebhookProviderIDReuse(t *testing.T) {
	s, _ := newTestServer()
	session := createTestSession(t, s)
	for _, version := range []int64{1, 2} {
		in := domain.GitHubPushConfigure{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, RequestID: "00000000-0000-0000-0000-000000000001", ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: "bot", WebhookSecret: domain.GitHubPushSecretRef{ID: "shared", Version: 1}, ProviderReadSecret: &domain.GitHubPushSecretRef{ID: "shared", Version: version}}}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "/api/v1/resources/missing/github-push-policy", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+session)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("shared purpose IDs reached resource mutation: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestGitHubPushClosedAutomaticReview(t *testing.T) {
	body := `{"expected_policy_generation":0,"expected_resource_generation":1,"expected_source_generation":1,"request_id":"00000000-0000-0000-0000-000000000001","reset_branch_baseline":true,"configuration":{"automation_actor_id":"bot","webhook_secret":{"id":"secret","version":1},"automatic_deployment":{"mode":"automatic_unprotected","expected_healthy_operation_id":"hop_a","expected_loaded_proxy_operation_id":"pop_a","expected_loaded_generation":1,"expected_runtime_configuration_generation":0,"container_port":80,"readiness":{"path":"/","timeout_seconds":5},"routes":[{"route_id":"route_a","expected_version":1,"expected_selected_release_operation_id":"hop_a"}]}}}`
	var valid domain.GitHubPushConfigure
	if !decodeGitHubPushCommand(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)), &valid) || valid.Validate() != nil {
		t.Fatal("valid existing-app review rejected")
	}
	for _, bad := range []string{strings.Replace(body, `"route_id":"route_a"`, `"Route_ID":"route_a"`, 1), strings.Replace(body, `"route_id":"route_a"`, `"route_id":"route_a","route_id":"route_b"`, 1), strings.Replace(body, `"route_id":"route_a"`, `"route_id":"route_a","host_port":12345`, 1), strings.Replace(body, `"routes":[{`, `"routes":[null,{`, 1), strings.Replace(body, `"expected_runtime_configuration_generation":0,`, "", 1), strings.Replace(body, `"automatic_deployment":{`, `"automatic_deployment":null,"unknown":{`, 1)} {
		var value domain.GitHubPushConfigure
		if decodeGitHubPushCommand(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/", strings.NewReader(bad)), &value) {
			t.Fatal("open or ambiguous automatic selector accepted")
		}
	}
}

func TestGitHubPushOwnerOptionsRegisteredGET(t *testing.T) {
	f := newPushHTTPFixture(t)
	path := "/api/v1/resources/" + f.resource.ID + "/github-push-policy"
	var owner domain.GitHubPushPolicy
	raw := f.request(http.MethodGet, path, f.owner, nil, http.StatusOK, &owner)
	if !owner.CanManage || owner.Status != "unconfigured" || owner.OwnerAutomaticDeploymentOptions == nil || owner.OwnerAutomaticDeploymentOptions.Available || owner.OwnerAutomaticDeploymentOptions.Baseline != nil || owner.OwnerAutomaticDeploymentOptions.Reason != "runner_unsupported" {
		t.Fatal("unconfigured owner options missing")
	}
	var closed domain.GitHubPushPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&closed); err != nil {
		t.Fatal("closed registered response DTO", err)
	}
	var viewer domain.GitHubPushPolicy
	raw = f.request(http.MethodGet, path, f.viewer, nil, http.StatusOK, &viewer)
	if viewer.CanManage || viewer.OwnerAutomaticDeploymentOptions != nil || bytes.Contains(raw, []byte("owner_automatic_deployment_options")) {
		t.Fatal("viewer review facts leaked")
	}
	in := domain.GitHubPushConfigure{ExpectedResourceGeneration: f.resource.Generation, ExpectedSourceGeneration: f.resource.Source.Git.Generation, RequestID: pushAPIUUID(190), ResetBranchBaseline: true, Configuration: domain.GitHubPushConfiguration{AutomationActorID: f.accountID, WebhookSecret: domain.GitHubPushSecretRef{ID: f.signing.ID, Version: 1}, ProviderReadSecret: &domain.GitHubPushSecretRef{ID: f.provider.ID, Version: 1}}}
	for n := 0; n < 2; n++ {
		var configured domain.GitHubPushPolicy
		raw = f.request(http.MethodPut, path, f.owner, in, http.StatusOK, &configured)
		if configured.OwnerAutomaticDeploymentOptions != nil || bytes.Contains(raw, []byte("owner_automatic_deployment_options")) {
			t.Fatal("mutation replay masquerades as fresh options")
		}
	}
}
