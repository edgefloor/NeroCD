package main

import (
	"errors"
	"strings"
)

// parseComposeImagePolicy accepts only the trusted runner-startup choices.
// It is intentionally separate from deployment plans and checked-out source.
func parseComposeImagePolicy(value string) (composeImagePolicy, error) {
	switch composeImagePolicy(strings.ToLower(strings.TrimSpace(value))) {
	case "", composeImagePolicyPreloaded:
		return composeImagePolicyPreloaded, nil
	case composeImagePolicyPull:
		return composeImagePolicyPull, nil
	default:
		return "", errors.New("compose image policy must be preloaded or pull")
	}
}
