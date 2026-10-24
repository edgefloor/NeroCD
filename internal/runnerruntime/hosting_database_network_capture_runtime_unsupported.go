//go:build !unix

package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// DatabaseEngineConfig is unavailable on unsupported hosts.
type DatabaseEngineConfig struct{}

// NewDatabaseEngineConfig fails closed on unsupported hosts.
func NewDatabaseEngineConfig(string, string) (DatabaseEngineConfig, error) {
	return DatabaseEngineConfig{}, errors.New("database execution requires native Linux arm64")
}

// DatabaseNetworkCaptureExecutionConfig enables one explicit read-only capture.
type DatabaseNetworkCaptureExecutionConfig struct {
	CaptureID string
	Engine    DatabaseEngineConfig
}

// Format redacts configuration on unsupported hosts too.
func (DatabaseNetworkCaptureExecutionConfig) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[database network capture configuration]")
}

// MarshalJSON refuses exporting protected configuration.
func (DatabaseNetworkCaptureExecutionConfig) MarshalJSON() ([]byte, error) {
	return nil, errors.New("database network capture configuration is not exportable")
}

func runDatabaseNetworkCaptureRuntime(context.Context, Config) error {
	return errors.New("database network capture requires native Linux arm64")
}
