package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func smoke(args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	email := fs.String("email", os.Getenv("NEROCD_SMOKE_EMAIL"), "bootstrap email")
	password := fs.String("password", os.Getenv("NEROCD_SMOKE_PASSWORD"), "bootstrap password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*email) == "" || *password == "" {
		return errors.New("smoke requires --email and --password (or NEROCD_SMOKE_EMAIL/NEROCD_SMOKE_PASSWORD)")
	}

	sessionBody, err := json.Marshal(map[string]string{"email": *email, "password": *password})
	if err != nil {
		return err
	}
	sessionReq, err := http.NewRequest(http.MethodPost, *addr+"/api/v1/sessions", bytes.NewReader(sessionBody))
	if err != nil {
		return err
	}
	sessionReq.Header.Set("Content-Type", "application/json")
	sessionToken, err := requestSessionToken(sessionReq)
	if err != nil {
		return err
	}
	fmt.Printf("ok %s %s\n", http.MethodPost, "/api/v1/sessions")

	checks := []struct {
		method string
		path   string
		body   string
		auth   bool
	}{
		{method: http.MethodGet, path: "/api/v1/health"},
		{method: http.MethodGet, path: "/api/v1/ready"},
		{method: http.MethodGet, path: "/api/v1/me", auth: true},
		{method: http.MethodGet, path: "/api/v1/projects", auth: true},
		{method: http.MethodGet, path: "/api/v1/templates", auth: true},
		{method: http.MethodGet, path: "/api/v1/runs", auth: true},
		{method: http.MethodGet, path: "/api/v1/run-logs?run_id=run_001", auth: true},
	}
	for _, check := range checks {
		var body io.Reader
		if check.body != "" {
			body = strings.NewReader(check.body)
		}
		req, err := http.NewRequest(check.method, *addr+check.path, body)
		if err != nil {
			return err
		}
		if check.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if check.auth {
			req.Header.Set("Authorization", "Bearer "+sessionToken)
		}
		if err := expectOK(req); err != nil {
			return err
		}
		fmt.Printf("ok %s %s\n", check.method, check.path)
	}
	if err := smokeOperatorWorkflow(*addr, sessionToken); err != nil {
		return err
	}
	return nil
}

func smokeOperatorWorkflow(addr string, sessionToken string) error {
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	tag := "smoke_" + suffix
	runPayload := map[string]any{
		"project_id": "proj_platform",
		"run_spec": map[string]any{
			"type":   "shell",
			"inputs": map[string]any{"command": "echo smoke"},
			"process": map[string]any{
				"command": []string{"echo", "smoke"},
			},
			"artifacts": []map[string]any{
				{"name": "smoke", "path": "smoke.txt", "required": false},
			},
		},
		"workflow": map[string]any{
			"steps": []map[string]any{
				{
					"id":   "prepare",
					"name": "Prepare",
					"run_spec": map[string]any{
						"type":   "shell",
						"inputs": map[string]any{"command": "echo prepare"},
						"process": map[string]any{
							"command": []string{"echo", "prepare"},
						},
					},
				},
				{
					"id":         "execute",
					"name":       "Execute",
					"depends_on": []string{"prepare"},
					"run_spec": map[string]any{
						"type":   "shell",
						"inputs": map[string]any{"command": "echo execute"},
						"process": map[string]any{
							"command": []string{"echo", "execute"},
						},
						"artifacts": []map[string]any{
							{"name": "smoke", "path": "smoke.txt", "required": false},
						},
					},
				},
			},
		},
		"runner_tags": []string{tag},
	}
	run, err := postAPI(addr+"/api/v1/runs", runPayload, sessionToken)
	if err != nil {
		return err
	}
	runID, err := requireString(run, "id")
	if err != nil {
		return fmt.Errorf("smoke run: %w", err)
	}

	registered, err := postAPI(addr+"/api/v1/runners/register", map[string]any{
		"id":           "runner_smoke_" + suffix,
		"name":         "Smoke Runner " + suffix,
		"tags":         []string{tag},
		"capabilities": []string{"shell"},
	}, sessionToken)
	if err != nil {
		return err
	}
	runnerToken, err := requireString(registered, "token")
	if err != nil {
		return fmt.Errorf("runner registration: %w", err)
	}

	for step := 1; step <= 2; step++ {
		claim, err := postAPI(addr+"/api/v1/runners/claim", map[string]any{}, runnerToken)
		if err != nil {
			return err
		}
		claimedRun, err := requireObject(claim, "run")
		if err != nil {
			return fmt.Errorf("claim %d: %w", step, err)
		}
		claimedRunID, err := requireString(claimedRun, "id")
		if err != nil {
			return fmt.Errorf("claim %d run: %w", step, err)
		}
		if claimedRunID != runID {
			return fmt.Errorf("claim %d returned run %s, want %s", step, claimedRunID, runID)
		}
		lease, err := requireObject(claim, "lease")
		if err != nil {
			return fmt.Errorf("claim %d: %w", step, err)
		}
		leaseID, err := requireString(lease, "id")
		if err != nil {
			return fmt.Errorf("claim %d lease: %w", step, err)
		}
		attempt, ok := lease["attempt"].(float64)
		if !ok {
			return errors.New("claim lease missing attempt")
		}
		fence, err := requireString(lease, "fence")
		if err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/logs", map[string]any{
			"run_id":   runID,
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"event_key": fmt.Sprintf("smoke_%s_event_%d", suffix, step),
			"sequence":  10 + step,
			"stream":    "stdout",
			"message":   fmt.Sprintf("smoke step %d", step),
		}, runnerToken); err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/artifacts", map[string]any{
			"run_id":   runID,
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"name":     fmt.Sprintf("smoke-step-%d", step),
			"path":     fmt.Sprintf("smoke-step-%d.txt", step),
			"found":    false,
			"required": false,
			"size":     0,
			"kind":     "file",
		}, runnerToken); err != nil {
			return err
		}
		if _, err := postAPI(addr+"/api/v1/runners/complete", map[string]any{
			"lease_id": leaseID,
			"attempt":  attempt, "fence": fence,
			"completion_key": fmt.Sprintf("smoke_%s_completion_%d", suffix, step),
			"status":         "succeeded",
		}, runnerToken); err != nil {
			return err
		}
	}

	runs, err := getAPI(addr+"/api/v1/runs?project_id=proj_platform&limit=1&offset=0", sessionToken)
	if err != nil {
		return err
	}
	if err := requirePagination(runs); err != nil {
		return fmt.Errorf("runs pagination: %w", err)
	}
	allRuns, err := getAPI(addr+"/api/v1/runs?project_id=proj_platform", sessionToken)
	if err != nil {
		return err
	}
	finalRun, err := findObjectByID(allRuns, runID)
	if err != nil {
		return err
	}
	status, err := requireString(finalRun, "status")
	if err != nil {
		return fmt.Errorf("final run: %w", err)
	}
	if status != "succeeded" {
		return fmt.Errorf("final run status = %s, want succeeded", status)
	}
	artifacts, err := getAPI(addr+"/api/v1/artifacts?run_id="+url.QueryEscape(runID), sessionToken)
	if err != nil {
		return err
	}
	if err := requirePagination(artifacts); err != nil {
		return fmt.Errorf("artifacts pagination: %w", err)
	}
	items, err := requireArray(artifacts, "items")
	if err != nil {
		return err
	}
	if len(items) < 2 {
		return fmt.Errorf("artifact list returned %d items, want at least 2", len(items))
	}
	fmt.Printf("ok operator workflow %s\n", runID)
	return nil
}

func requestSessionToken(req *http.Request) (string, error) {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%s %s returned %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", errors.New("session response did not include a token")
	}
	return payload.Token, nil
}
