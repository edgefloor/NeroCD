package domain

import "testing"

func TestGitHubPushSourceIsExactGitHubHTTPSBranch(t *testing.T) {
	for _, tc := range []struct {
		url, ref, kind string
		valid          bool
	}{
		{"https://github.com/acme/app.git", "main", "public", true},
		{"https://github.com/acme/app", "refs/heads/release/stable", "https_basic", true},
		{"https://github.com.evil/acme/app", "main", "public", false},
		{"https://token@github.com/acme/app", "main", "public", false},
		{"http://github.com/acme/app", "main", "public", false},
		{"https://github.com/acme/app", "refs/tags/v1", "public", false},
		{"https://github.com/acme/app", "main*", "public", false},
		{"https://github.com/acme/app", "main", "ssh_private_key", false},
		{"https://github.com/acme/app?token=x", "main", "public", false},
	} {
		_, _, _, err := GitHubPushRepositoryBranch(HostingGitSource{RepositoryURL: tc.url, RequestedRef: tc.ref, CredentialKind: tc.kind})
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}

func TestGitHubPushSecretPurposesRequireDistinctIDs(t *testing.T) {
	for _, tc := range []struct {
		name, providerID string
		providerVersion  int64
		valid            bool
	}{
		{"same ID and version", "webhook", 1, false},
		{"same ID different version", "webhook", 2, false},
		{"different IDs", "provider", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := GitHubPushConfigure{ExpectedResourceGeneration: 1, ExpectedSourceGeneration: 1, RequestID: "00000000-0000-0000-0000-000000000001", ResetBranchBaseline: true, Configuration: GitHubPushConfiguration{AutomationActorID: "bot", WebhookSecret: GitHubPushSecretRef{ID: "webhook", Version: 1}, ProviderReadSecret: &GitHubPushSecretRef{ID: tc.providerID, Version: tc.providerVersion}}}
			if err := in.Validate(); (err == nil) != tc.valid {
				t.Fatalf("distinct secret ID validation=%v, valid=%v", err, tc.valid)
			}
		})
	}
}
