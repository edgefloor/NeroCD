package runnerclient

import (
	"context"
	"errors"
	"strings"
)

// Identity resolves the runner ID belonging to this dedicated credential.
// It makes no heartbeat or registration request and never guesses on failure.
func (c Client) Identity(ctx context.Context) (string, error) {
	var result struct {
		RunnerID string `json:"runner_id"`
	}
	if err := c.get(ctx, "/api/v1/runners/identity", &result); err != nil {
		return "", err
	}
	if result.RunnerID == "" || strings.TrimSpace(result.RunnerID) != result.RunnerID {
		return "", errors.New("runner identity response did not include a valid runner_id")
	}
	return result.RunnerID, nil
}
