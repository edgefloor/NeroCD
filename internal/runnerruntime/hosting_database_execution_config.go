package runnerruntime

import (
	"errors"
	"fmt"
	"io"

	"nerocd/internal/runnerclient"
)

// DatabaseExecutionConfig opts into the private database executor. Roots must
// already exist on the native daemon host. Loopback HTTP is for fixtures only.
type DatabaseExecutionConfig struct {
	StateRoot, SecretRoot string
	TrustedLoopbackHTTP   bool
	NetworkUpgrade        bool
	NetworkCapture        bool
	ApplicationDatabase   bool
	RouteContinuity       bool
	ApplicationStorage    bool
	MembershipMaintenance bool
	LogicalBackups        bool
}

// Format redacts protected host paths for every formatting verb.
func (DatabaseExecutionConfig) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[database execution configuration]")
}

// MarshalJSON refuses serialization to keep protected host paths out of output.
func (DatabaseExecutionConfig) MarshalJSON() ([]byte, error) {
	return nil, errors.New("database execution configuration is not exportable")
}

func validateDatabaseExecutionConfig(c Config) error {
	if err := validateAutomaticDatabaseNetworkCaptureConfig(c); err != nil {
		return err
	}
	if c.Database == nil {
		return nil
	}
	if c.Database.ApplicationStorage && !c.Database.ApplicationDatabase {
		return errors.New("connected application storage requires connected execution")
	}
	if c.Database.RouteContinuity && (!c.Database.ApplicationDatabase || !c.Proxy.Enabled() || !c.Proxy.VerifiedSwitch) {
		return errors.New("application database route continuity requires connected execution and strict proxy switching")
	}
	if c.Database.ApplicationDatabase || c.Database.MembershipMaintenance {
		if !c.Database.NetworkCapture || !c.Database.NetworkUpgrade || c.RuntimeEnvironment == nil || c.DatabaseNetworkCapture != nil || c.JournalDir == "" || c.Token == "" || c.Database.TrustedLoopbackHTTP || c.RuntimeEnvironment.TrustedLoopbackHTTP {
			return errors.New("connected database execution requires protected runtime delivery, automatic capture, upgrade and explicit journal configuration")
		}
	}
	if !c.Execute || c.CompleteStatus != "" {
		return errors.New("database execution requires execution and forbids generic completion")
	}
	if err := runnerclient.ValidateRuntimeEnvironmentServer(c.Server, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: c.Database.TrustedLoopbackHTTP}); err != nil {
		return errors.New("database execution requires protected transport")
	}
	return validateDatabasePlatformConfig(*c.Database)
}
