package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var runnerHTTPClient = &http.Client{Timeout: 5 * time.Second}

func randomRuntimeHex(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func callAPI(args []string, path string) error {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	token := fs.String("token", os.Getenv("NEROCD_TOKEN"), "NeroCD bearer token")
	if err := fs.Parse(args); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, *addr+path, nil)
	if err != nil {
		return err
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w\nhint: set --addr or NEROCD_ADDR to the server URL, for example http://127.0.0.1:18080", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// runLogRetention is intentionally a small authenticated operator client, not
// a direct database shortcut.  Execute requires a caller-provided stable
// request ID so an interrupted invocation can safely replay its receipt.
func runLogRetention(args []string) error {
	if len(args) == 0 {
		return errors.New("run-log-retention requires status, preview, update, or execute")
	}
	action := args[0]
	fs := flag.NewFlagSet("run-log-retention", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	token := fs.String("token", os.Getenv("NEROCD_TOKEN"), "system-admin bearer token")
	enabled := fs.Bool("enabled", false, "enable retention when updating")
	keepDays := fs.Int("keep-days", 30, "retention days when updating")
	batchSize := fs.Int("batch-size", 1000, "maximum logs per execution")
	policyVersion := fs.Int("policy-version", 0, "previewed policy version for execute")
	requestID := fs.String("request-id", "", "stable request ID for execute replay")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return errors.New("run-log-retention requires --token or NEROCD_TOKEN")
	}
	method, path := http.MethodGet, "/api/v1/run-log-retention"
	var payload any
	switch action {
	case "status":
	case "preview":
		method, path, payload = http.MethodPost, "/api/v1/run-log-retention/preview", map[string]any{}
	case "update":
		method, payload = http.MethodPut, map[string]any{"enabled": *enabled, "keep_days": *keepDays, "batch_size": *batchSize}
	case "execute":
		if *policyVersion < 1 || strings.TrimSpace(*requestID) == "" {
			return errors.New("run-log-retention execute requires --policy-version and --request-id")
		}
		method, path, payload = http.MethodPost, "/api/v1/run-log-retention/execute", map[string]any{"policy_version": *policyVersion}
	default:
		return errors.New("run-log-retention requires status, preview, update, or execute")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, strings.TrimRight(*addr, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if action == "execute" {
		req.Header.Set("X-Request-ID", *requestID)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return errors.New("run-log-retention request failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("run-log-retention returned %s", response.Status)
	}
	var result any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return errors.New("run-log-retention response was invalid")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// callReady is intentionally not a generic successful-HTTP probe. Readiness
// means the database-backed service accepted the readiness query and returned
// the exact stable envelope used by the orchestration profile.
func callReady(args []string) error {
	fs := flag.NewFlagSet("ready", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, *addr+"/api/v1/ready", nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%w\nhint: set --addr or NEROCD_ADDR to the server URL, for example http://127.0.0.1:18080", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned %s", resp.Status)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload); err != nil {
		return fmt.Errorf("decode readiness response: %w", err)
	}
	if payload.Status != "ready" {
		return fmt.Errorf("readiness response status = %q, want ready", payload.Status)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "ready"})
}

func postAPI(url string, body any, token string) (map[string]any, error) {
	var result map[string]any
	if err := postAPIInto(url, body, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func postAPIInto(url string, body any, token string, result any) error {
	return postAPIIntoContext(context.Background(), url, body, token, result)
}

func postAPINoResponse(url string, body any, token string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := runnerHTTPClient.Do(req)
	if err != nil {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil
}
func postAPIIntoContext(ctx context.Context, url string, body any, token string, result any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := runnerHTTPClient.Do(req)
	if err != nil {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), StatusCode: resp.StatusCode, Status: resp.Status, Detail: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return &runnerAPIError{Method: http.MethodPost, URL: runnerRequestLabel(url), Err: err}
	}
	return nil
}

func getAPI(url string, token string) (map[string]any, error) {
	var result map[string]any
	if err := getAPIInto(url, token, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func getAPIInto(url string, token string, result any) error {
	return getAPIIntoContext(context.Background(), url, token, result)
}
func getAPIIntoContext(ctx context.Context, url string, token string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := runnerHTTPClient.Do(req)
	if err != nil {
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), StatusCode: resp.StatusCode, Status: resp.Status, Detail: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return &runnerAPIError{Method: http.MethodGet, URL: runnerRequestLabel(url), Err: err}
	}
	return nil
}

func requireString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("missing string field %q", key)
	}
	return value, nil
}

func requireObject(payload map[string]any, key string) (map[string]any, error) {
	value, ok := payload[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing object field %q", key)
	}
	return value, nil
}

func requireArray(payload map[string]any, key string) ([]any, error) {
	value, ok := payload[key].([]any)
	if !ok {
		return nil, fmt.Errorf("missing array field %q", key)
	}
	return value, nil
}

func requirePagination(payload map[string]any) error {
	for _, key := range []string{"limit", "offset", "count", "total"} {
		if _, ok := payload[key].(float64); !ok {
			return fmt.Errorf("missing numeric field %q", key)
		}
	}
	if _, err := requireArray(payload, "items"); err != nil {
		return err
	}
	return nil
}

func findObjectByID(payload map[string]any, id string) (map[string]any, error) {
	items, err := requireArray(payload, "items")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemID, _ := object["id"].(string)
		if itemID == id {
			return object, nil
		}
	}
	return nil, fmt.Errorf("items did not include id %s", id)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func createSession(args []string) error {
	fs := flag.NewFlagSet("session", flag.ExitOnError)
	addr := fs.String("addr", defaultAPIAddr(), "NeroCD server URL")
	email := fs.String("email", "", "user email")
	password := fs.String("password", os.Getenv("NEROCD_PASSWORD"), "user password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*email) == "" || *password == "" {
		return errors.New("session requires --email and --password")
	}
	body := bytes.NewBufferString(fmt.Sprintf(`{"email":%q,"password":%q}`, *email, *password))
	req, err := http.NewRequest(http.MethodPost, *addr+"/api/v1/sessions", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return printAPIResponse(req)
}

func printAPIResponse(req *http.Request) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errors.New(resp.Status)
	}
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func expectOK(req *http.Request) error {
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s returned %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func defaultAPIAddr() string {
	if addr := os.Getenv("NEROCD_ADDR"); addr != "" {
		return addr
	}
	return "http://127.0.0.1:8080"
}
