package source

import "testing"

func TestValidateRepositoryURLAllowsNetworkGitURLs(t *testing.T) {
	tests := []string{
		"https://github.com/example/repo.git",
		"ssh://git@github.com/example/repo.git",
		"git://github.com/example/repo.git",
		"git@github.com:example/repo.git",
		"github.com:example/repo.git",
	}
	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			if err := ValidateRepositoryURL(test); err != nil {
				t.Fatalf("ValidateRepositoryURL(%q) returned %v", test, err)
			}
		})
	}
}

func TestValidateRepositoryURLRejectsUnsafeSources(t *testing.T) {
	tests := []string{
		"",
		"/tmp/repo",
		"../repo",
		"file:///tmp/repo",
		"https://localhost/repo.git",
		"https://127.0.0.1/repo.git",
		"https://[::1]/repo.git",
		"https://10.0.0.1/repo.git",
		"https://172.16.0.1/repo.git",
		"https://192.168.1.1/repo.git",
		"https://169.254.169.254/latest/meta-data",
		"https://169.254.10.20/repo.git",
		"https://0.0.0.0/repo.git",
	}
	for _, test := range tests {
		t.Run(test, func(t *testing.T) {
			if err := ValidateRepositoryURL(test); err == nil {
				t.Fatalf("ValidateRepositoryURL(%q) returned nil, want error", test)
			}
		})
	}
}
