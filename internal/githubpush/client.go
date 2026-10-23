// Package githubpush verifies only fixed GitHub.com repository and branch state.
package githubpush

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"nerocd/internal/domain"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion pins the verified GitHub REST contract.
const APIVersion = "2026-03-10"

// ErrUnavailable is closed and never contains a credential, URL or provider body.
var ErrUnavailable = errors.New("github_provider_unavailable")

// Failure reports actionable closed verification reasons.
type Failure struct{ Reason string }

func (e *Failure) Error() string { return e.Reason }

// Request is server-derived frozen repository identity and desired predecessor.
type Request struct {
	Owner, Repository, Branch string
	RepositoryID              int64
	DesiredCommit             string
	Private                   bool
}

// Evidence is a normalized proof bound to an immutable comparison request.
type Evidence struct {
	RepositoryID                                      int64
	Repository, Branch, Head, Base, MergeBase, Status string
	Private                                           bool
	AheadBy, BehindBy                                 int
}

// Client owns a fixed-host bounded GitHub REST transport.
type Client struct{ http *http.Client }

// New constructs a fixed-origin client. A supplied trusted client supports controlled TLS fixtures.
func New(client *http.Client) (*Client, error) {
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, ok := transport.(*http.Transport)
	if !ok || base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify {
		return nil, ErrUnavailable
	}
	clone := base.Clone()
	clone.Proxy = nil
	copy.Transport = clone
	copy.Jar = nil
	copy.Timeout = 5 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copy}, nil
}

func (c *Client) get(ctx context.Context, path string, credential []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	if len(credential) > 0 {
		req.Header.Set("Authorization", "Bearer "+string(credential))
	}
	response, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == 429 || response.StatusCode == 403 && response.Header.Get("X-RateLimit-Remaining") == "0" {
		return &Failure{Reason: "rate_limited"}
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return &Failure{Reason: "provider_credentials_required"}
	}
	if response.StatusCode == 404 {
		return &Failure{Reason: "repository_or_branch_unavailable"}
	}
	if response.StatusCode != 200 {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return &Failure{Reason: "provider_response_invalid"}
	}
	if !json.Valid(raw) || json.Unmarshal(raw, out) != nil {
		return &Failure{Reason: "provider_response_invalid"}
	}
	return nil
}

// Verify proves current branch HEAD and a bounded equal/fast-forward relation.
// Comparisons beyond100 commits block rather than inferring ancestry from counts.
func (c *Client) Verify(ctx context.Context, in Request, credential []byte) (Evidence, error) {
	var zero Evidence
	if in.Owner == "" || in.Repository == "" || in.Branch == "" || strings.ContainsAny(in.Owner+in.Repository, "/%?#") || in.Private && len(credential) == 0 {
		return zero, &Failure{Reason: "provider_credentials_required"}
	}
	base := "/repos/" + url.PathEscape(in.Owner) + "/" + url.PathEscape(in.Repository)
	var repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Private  *bool  `json:"private"`
		Fork     *bool  `json:"fork"`
	}
	if err := c.get(ctx, base, credential, &repository); err != nil {
		return zero, err
	}
	if repository.ID < 1 || repository.Private == nil || repository.Fork == nil || *repository.Fork || !strings.EqualFold(repository.FullName, in.Owner+"/"+in.Repository) || in.RepositoryID != 0 && in.RepositoryID != repository.ID || in.Private != *repository.Private {
		return zero, &Failure{Reason: "repository_identity_mismatch"}
	}
	var branch struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := c.get(ctx, base+"/branches/"+url.PathEscape(in.Branch), credential, &branch); err != nil {
		return zero, err
	}
	if branch.Name != in.Branch || !domain.ValidGitHubPushCommit(branch.Commit.SHA) {
		return zero, &Failure{Reason: "provider_response_invalid"}
	}
	proof := Evidence{RepositoryID: repository.ID, Repository: repository.FullName, Private: *repository.Private, Branch: branch.Name, Head: branch.Commit.SHA, Status: "initial"}
	if in.DesiredCommit == "" {
		return proof, nil
	}
	if !domain.ValidGitHubPushCommit(in.DesiredCommit) {
		return zero, &Failure{Reason: "provider_response_invalid"}
	}
	if in.DesiredCommit == proof.Head {
		proof.Status = "identical"
		proof.Base = proof.Head
		proof.MergeBase = proof.Head
		return proof, nil
	}
	var comparison struct {
		Base struct {
			SHA string `json:"sha"`
		} `json:"base_commit"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Status  string `json:"status"`
		Ahead   *int   `json:"ahead_by"`
		Behind  *int   `json:"behind_by"`
		Total   *int   `json:"total_commits"`
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	}
	if err := c.get(ctx, base+"/compare/"+in.DesiredCommit+"..."+proof.Head+"?per_page=100&page=1", credential, &comparison); err != nil {
		return zero, err
	}
	if comparison.Ahead == nil || comparison.Behind == nil || comparison.Total == nil {
		return zero, &Failure{Reason: "provider_response_invalid"}
	}
	if *comparison.Ahead > domain.GitHubPushComparisonLimit {
		return zero, &Failure{Reason: "comparison_limit"}
	}
	if comparison.Base.SHA != in.DesiredCommit || comparison.MergeBase.SHA != in.DesiredCommit || comparison.Status != "ahead" || *comparison.Behind != 0 || *comparison.Ahead < 1 || *comparison.Total != *comparison.Ahead || len(comparison.Commits) != *comparison.Ahead || comparison.Commits[len(comparison.Commits)-1].SHA != proof.Head {
		return zero, &Failure{Reason: "ancestry_unverified"}
	}
	for _, commit := range comparison.Commits {
		if !domain.ValidGitHubPushCommit(commit.SHA) {
			return zero, &Failure{Reason: "provider_response_invalid"}
		}
	}
	proof.Base = comparison.Base.SHA
	proof.MergeBase = comparison.MergeBase.SHA
	proof.Status = comparison.Status
	proof.AheadBy = *comparison.Ahead
	proof.BehindBy = *comparison.Behind
	return proof, nil
}
