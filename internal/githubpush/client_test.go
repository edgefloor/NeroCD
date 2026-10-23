package githubpush

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"nerocd/internal/domain"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyImmutableBoundedComparison(t *testing.T) {
	before, after := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, mode := range []string{"valid", "equal", "initial", "bad_repo", "fork", "wrong_branch", "wrong_head", "wrong_base", "wrong_merge", "missing_count", "behind", "too_many", "rate_limit", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-GitHub-Api-Version") != APIVersion {
					t.Error("wrong fixed provider boundary")
				}
				if mode == "rate_limit" {
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, "https://foreign.invalid", http.StatusMovedPermanently)
					return
				}
				var body any
				switch r.URL.Path {
				case "/repos/acme/app":
					id := int64(42)
					if mode == "bad_repo" {
						id = 43
					}
					body = map[string]any{"id": id, "full_name": "acme/app", "private": true, "fork": mode == "fork"}
				case "/repos/acme/app/branches/main":
					branch := "main"
					if mode == "wrong_branch" {
						branch = "other"
					}
					body = map[string]any{"name": branch, "commit": map[string]any{"sha": after}}
				case "/repos/acme/app/compare/" + before + "..." + after:
					if r.URL.RawQuery != "per_page=100&page=1" {
						t.Error(r.URL.RawQuery)
					}
					base, merge, head := before, before, after
					status := "ahead"
					ahead := 1
					behind := 0
					if mode == "wrong_base" {
						base = after
					}
					if mode == "wrong_merge" {
						merge = after
					}
					if mode == "wrong_head" {
						head = before
					}
					if mode == "behind" {
						status = "behind"
						behind = 1
					}
					if mode == "too_many" {
						ahead = 101
					}
					comparison := map[string]any{"base_commit": map[string]any{"sha": base}, "merge_base_commit": map[string]any{"sha": merge}, "status": status, "ahead_by": ahead, "behind_by": behind, "total_commits": ahead, "commits": []map[string]any{{"sha": head}}}
					if mode == "missing_count" {
						delete(comparison, "ahead_by")
					}
					body = comparison
				default:
					t.Error("unexpected URL", r.URL)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if err := json.NewEncoder(w).Encode(body); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			httpClient := server.Client()
			transport := httpClient.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.ServerName = "example.com"
			address := strings.TrimPrefix(server.URL, "https://")
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			httpClient.Transport = transport
			client, err := New(httpClient)
			if err != nil {
				t.Fatal(err)
			}
			desired := before
			if mode == "equal" {
				desired = after
			}
			if mode == "initial" {
				desired = ""
			}
			evidence, err := client.Verify(t.Context(), Request{Owner: "acme", Repository: "app", Branch: "main", RepositoryID: 42, Private: true, DesiredCommit: desired}, []byte("fixture-token"))
			valid := mode == "valid" || mode == "equal" || mode == "initial"
			if (err == nil) != valid {
				t.Fatal(evidence, err)
			}
			if err != nil && strings.Contains(err.Error(), "fixture-token") {
				t.Fatal("credential in failure")
			}
			if valid && (evidence.Head != after || evidence.RepositoryID != 42) {
				t.Fatal(evidence)
			}
		})
	}
}
func TestRejectInsecureProviderTransport(t *testing.T) {
	if _, err := New(&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}); err == nil {
		t.Fatal("insecure transport accepted")
	}
	client, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Verify(t.Context(), Request{Owner: "acme", Repository: "app", Branch: "main", Private: true}, nil); err == nil {
		t.Fatal("missing credential used as anonymous")
	}
}

func TestSignatureOfficialVectorAndMalformedHeaders(t *testing.T) {
	key := []byte("It's a Secret to Everybody")
	raw := []byte("Hello, World!")
	signature := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if !VerifySignature(key, raw, signature) {
		t.Fatal("official HMAC vector rejected")
	}
	for _, header := range []string{"", signature + " ", " " + signature, strings.ToUpper(signature), strings.Replace(signature, "sha256=", "sha256:", 1), signature + "," + signature, "sha256=" + strings.Repeat("a", 64)} {
		if VerifySignature(key, raw, header) {
			t.Fatal("malformed or invalid signature accepted")
		}
	}
	if VerifySignature(key, append(raw, '!'), signature) || VerifySignature([]byte("different"), raw, signature) || VerifySignature(nil, raw, signature) || VerifySignature(key, make([]byte, domain.GitHubPushPayloadLimit+1), signature) {
		t.Fatal("unbound signature accepted")
	}
}
