package runnerruntime

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
)

// StorageArchiveExecutionConfig independently opts into destination-local
// archives. The helper descriptor must come from an explicitly qualified local
// image; its presence alone does not qualify the runner or advertise capability.
type StorageArchiveExecutionConfig struct {
	RemoteBackups                             bool
	RemoteEndpoints, RemoteCIDRs              []string
	RemoteFixtureOrigin                       string
	ArtifactRoot, TemporaryRoot, DockerBinary string
	Helper                                    domain.HostingStorageArchiveHelper
	TrustedLoopbackHTTP                       bool
}

// Format redacts protected filesystem configuration for every formatting verb.
func (StorageArchiveExecutionConfig) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[storage archive execution configuration]")
}

// MarshalJSON refuses serialization of protected host configuration.
func (StorageArchiveExecutionConfig) MarshalJSON() ([]byte, error) {
	return nil, errors.New("storage archive execution configuration is not exportable")
}

func (c StorageArchiveExecutionConfig) validate() error {
	if _, err := storageBackupRemotePolicy(c); err != nil {
		return err
	}
	if c.Helper.Validate() != nil {
		return errors.New("storage archive requires an exact local helper identity")
	}
	for _, path := range []string{c.ArtifactRoot, c.TemporaryRoot, c.DockerBinary} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, ",\r\n\x00") {
			return errors.New("storage archive requires explicit protected local paths")
		}
	}
	if c.ArtifactRoot == c.TemporaryRoot || strings.HasPrefix(c.ArtifactRoot, c.TemporaryRoot+"/") || strings.HasPrefix(c.TemporaryRoot, c.ArtifactRoot+"/") {
		return errors.New("storage archive roots must be separate")
	}
	return nil
}
