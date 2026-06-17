package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nerocd/internal/domain"
)

type ArtifactResult struct {
	Name     string
	Path     string
	Found    bool
	Required bool
	Size     int64
	IsDir    bool
}

func CaptureArtifacts(baseDir string, artifacts []domain.ArtifactSpec, emit func(ProcessEvent)) ([]ArtifactResult, error) {
	if len(artifacts) == 0 {
		return nil, nil
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, errors.New("artifact base directory is required")
	}
	if emit == nil {
		emit = func(ProcessEvent) {}
	}

	results := make([]ArtifactResult, 0, len(artifacts))
	var missingRequired []string
	for _, artifact := range artifacts {
		name := strings.TrimSpace(artifact.Name)
		path := strings.TrimSpace(artifact.Path)
		if name == "" || path == "" {
			return results, errors.New("artifact name and path are required")
		}
		cleanPath, err := cleanRelativePath(path, "artifact path")
		if err != nil {
			return results, err
		}
		absPath := filepath.Join(baseDir, cleanPath)
		info, err := os.Stat(absPath)
		result := ArtifactResult{Name: name, Path: cleanPath, Required: artifact.Required}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				results = append(results, result)
				if artifact.Required {
					missingRequired = append(missingRequired, name)
					emit(ProcessEvent{Stream: domain.LogSystem, Message: fmt.Sprintf("Required artifact %q missing at %s", name, cleanPath)})
				} else {
					emit(ProcessEvent{Stream: domain.LogSystem, Message: fmt.Sprintf("Optional artifact %q missing at %s", name, cleanPath)})
				}
				continue
			}
			return results, err
		}
		result.Found = true
		result.Size = info.Size()
		result.IsDir = info.IsDir()
		results = append(results, result)
		kind := domain.ArtifactFile
		if info.IsDir() {
			kind = domain.ArtifactDirectory
		}
		emit(ProcessEvent{Stream: domain.LogSystem, Message: fmt.Sprintf("Captured artifact %q (%s) at %s", name, kind, cleanPath)})
	}
	if len(missingRequired) > 0 {
		return results, fmt.Errorf("missing required artifacts: %s", strings.Join(missingRequired, ", "))
	}
	return results, nil
}

func cleanRelativePath(value string, label string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(value))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s must be a relative child path", label)
	}
	return clean, nil
}
