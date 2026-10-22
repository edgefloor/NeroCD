package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const maxIdentityResponseBytes = 1 << 20

type identityResponse struct {
	RunnerID string `json:"runner_id"`
}

// decodeResponse validates the entire bounded identity document before the CLI
// can use its authority. Limit+1 distinguishes a complete body at the limit
// from a longer body whose first JSON value happens to fit.
func (r *identityResponse) decodeResponse(body io.Reader) error {
	contents, err := io.ReadAll(io.LimitReader(body, maxIdentityResponseBytes+1))
	if err != nil {
		return err
	}
	if len(contents) > maxIdentityResponseBytes {
		return errors.New("runner identity response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var result identityResponse
	if err = decoder.Decode(&result); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("runner identity response must contain exactly one JSON document")
	}
	*r = result
	return nil
}

// Identity resolves the runner ID belonging to this dedicated credential.
// It makes no heartbeat or registration request and never guesses on failure.
func (c Client) Identity(ctx context.Context) (string, error) {
	var result identityResponse
	if err := c.get(ctx, "/api/v1/runners/identity", &result); err != nil {
		return "", err
	}
	if result.RunnerID == "" || strings.TrimSpace(result.RunnerID) != result.RunnerID {
		return "", errors.New("runner identity response did not include a valid runner_id")
	}
	return result.RunnerID, nil
}
