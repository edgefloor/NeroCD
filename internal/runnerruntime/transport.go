package runnerruntime

import (
	"nerocd/internal/runnerclient"
)

type runnerAPIError = runnerclient.APIError
type runnerFailureClass = runnerclient.FailureClass

const (
	runnerFailurePermanent = runnerclient.FailurePermanent
	runnerFailureTransient = runnerclient.FailureTransient
	runnerFailureAuthority = runnerclient.FailureAuthority
)

func classifyRunnerFailure(err error) runnerFailureClass { return runnerclient.ClassifyFailure(err) }
func runnerHTTPStatus(err error, status int) bool        { return runnerclient.HTTPStatus(err, status) }
func runnerRequestLabel(rawURL string) string            { return runnerclient.RequestLabel(rawURL) }
