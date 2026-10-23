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
