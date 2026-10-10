package runnerruntime

import (
	"errors"
	"regexp"
	"strings"
)

var productionImageDigest = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$`)

func validateProductionImageReference(value string) error {
	if value == "" || !productionImageDigest.MatchString(value) {
		return errors.New("production requires image reference as repository@sha256 digest")
	}
	prefix := strings.SplitN(value, "@", 2)[0]
	if strings.LastIndex(prefix, ":") > strings.LastIndex(prefix, "/") {
		return errors.New("production rejects tagged image reference")
	}
	return nil
}

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
