package source

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ControlledGitCheckout acquires a detached commit using an already admitted
// address. The caller supplies an isolated command environment and owns the
// workspace and command cancellation. SSH callers must configure a pinned SSH
// transport; HTTP(S) uses the address directly and never follows redirects.
func ControlledGitCheckout(ctx context.Context, workspace string, repositoryURL *url.URL, pinnedIP netip.Addr, requestedRef string, run func(context.Context, string, []string, string) ([]byte, error)) (string, error) {
	// Detached init/fetch avoids remote tracking refs and leaves no branch name
	// that could move after the policy-checked fetch.
	gitBase := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "http.proxy=", "-c", "init.templateDir="}
	if _, err := run(ctx, "git init", append(append([]string{}, gitBase...), "init", "--quiet", workspace), ""); err != nil {
		return "", err
	}
	if _, err := run(ctx, "git remote add", append(append([]string{}, gitBase...), "remote", "add", "origin", TransportURL(repositoryURL)), workspace); err != nil {
		return "", err
	}
	fetchArgs := append(append([]string{}, gitBase...), "fetch", "--no-tags", "--depth=1", "origin", requestedRef)
	if repositoryURL.Scheme != "ssh" {
		resolveOption, err := GitCurlResolveOption(repositoryURL, pinnedIP)
		if err != nil {
			return "", err
		}
		fetchArgs = append(append([]string{}, gitBase...), "-c", "http.curloptResolve="+resolveOption, "fetch", "--no-tags", "--depth=1", "origin", requestedRef)
	}
	if _, err := run(ctx, "git fetch", fetchArgs, workspace); err != nil {
		return "", err
	}
	if _, err := run(ctx, "git checkout", append(append([]string{}, gitBase...), "checkout", "--detach", "--quiet", "FETCH_HEAD"), workspace); err != nil {
		return "", err
	}
	head, err := run(ctx, "git rev-parse", []string{"rev-parse", "--verify", "HEAD^{commit}"}, workspace)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(head))
	if !validGitCommit(commit) {
		return "", errors.New("checkout did not produce a full immutable commit")
	}
	return commit, nil
}

// GitCurlResolveAvailable checks the installed Git supported-variable table.
// Git permits arbitrary config keys, so `git config --get` is
// not a feature probe. `git help --config` is generated from this installed
// Git's supported-variable table and lets us fail closed before any network
// traffic on releases without the libcurl resolve bridge.
func GitCurlResolveAvailable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeCtx, "git", "help", "--config")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	output, err := command.Output()
	return err == nil && bytes.Contains(output, []byte("http.curloptResolve\n"))
}

// GitCurlResolveOption produces the libcurl CURLOPT_RESOLVE syntax accepted by
// modern Git.  It connects to this literal IP while retaining the URL hostname
// for TLS verification/SNI and HTTP Host.  The option is deliberately passed
// only to fetch; no global config is mutated.
func GitCurlResolveOption(repositoryURL *url.URL, address netip.Addr) (string, error) {
	if repositoryURL == nil || repositoryURL.Hostname() == "" || !address.IsValid() {
		return "", errors.New("controlled Git transport is unavailable")
	}
	port := 443
	if repositoryURL.Scheme == "http" {
		port = 80
	}
	if raw := repositoryURL.Port(); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 65535 {
			return "", errors.New("repository port is invalid")
		}
		port = parsed
	}
	ip := address.String()
	if address.Is6() {
		ip = "[" + ip + "]"
	}
	// CURLOPT_RESOLVE must match the hostname spelling used by the fetch URL.
	// A terminal DNS dot is significant to curl's lookup key even though policy
	// host matching treats it as equivalent. Removing it bypasses this pin.
	return repositoryURL.Hostname() + ":" + strconv.Itoa(port) + ":" + ip, nil
}

func validGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
