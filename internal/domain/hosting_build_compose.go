package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Git Compose versions gate prepared results and runner execution separately from pasted stacks.
const (
	HostingGitComposeArgsCapability  = "hosting_git_compose_args_v1"
	HostingGitComposeBuildCapability = "hosting_git_compose_build_v1"
	HostingGitComposeVersion         = "git-compose.v1"
)

// HostingStackBuild retains only a confined Dockerfile build, without caller shell or secrets.
type HostingStackBuild struct {
	Args       map[string]string `json:"args,omitempty"`
	Context    string            `json:"context"`
	Dockerfile string            `json:"dockerfile"`
	Target     string            `json:"target,omitempty"`
}

// HostingBuildComposeService binds one build-owning component to its local artifact.
type HostingBuildComposeService struct {
	Component string               `json:"component"`
	Artifact  HostingBuildArtifact `json:"artifact"`
}

// HostingBuildComposeResult is the immutable prepared stack from one Git source receipt.
type HostingBuildComposeResult struct {
	SourceReceiptHash string                       `json:"source_receipt_hash"`
	Source            HostingStackSource           `json:"source"`
	Definition        HostingStackDefinition       `json:"definition"`
	Services          []HostingBuildComposeService `json:"services"`
	Hash              string                       `json:"hash"`
}

// HostingComposeBuildID gives each component a stable engine identity within its parent build.
func HostingComposeBuildID(parentBuildID, component string) string {
	sum := sha256.Sum256([]byte(HostingGitComposeVersion + "\x00" + parentBuildID + "\x00" + component))
	return "cmp-" + hex.EncodeToString(sum[:])[:48]
}

// ParseHostingGitCompose permits builds only within this explicit source profile.
func ParseHostingGitCompose(source HostingStackSource, settings HostingBuildSettings) (HostingStackDefinition, error) {
	if settings.Strategy != "compose" || ValidateHostingBuildSettings(settings) != nil || source.PublicComponent != settings.PublicComponent || source.ContainerPort != settings.ContainerPort {
		return HostingStackDefinition{}, errors.New("git Compose settings changed")
	}
	definition, err := parseHostingStack(source, &settings)
	if err != nil {
		return definition, err
	}
	definition.Version = HostingGitComposeVersion
	for _, service := range definition.Services {
		if _, err = HostingStackBuildOwner(definition, service.Name); err != nil {
			return HostingStackDefinition{}, err
		}
	}
	return definition, nil
}

func parseHostingStackBuild(node *yaml.Node, settings HostingBuildSettings) (*HostingStackBuild, error) {
	context := ""
	dockerfile := "Dockerfile"
	target := ""
	var err error
	var argsNode *yaml.Node
	if node.Kind == yaml.ScalarNode {
		context, err = stackString(node)
	} else {
		fields, e := stackFields(node, "build", "context", "dockerfile", "target", "args")
		if e != nil {
			return nil, e
		}
		argsNode = fields["args"]
		context = "."
		if fields["context"] != nil {
			context, err = stackString(fields["context"])
		}
		if fields["dockerfile"] != nil {
			dockerfile, err = stackString(fields["dockerfile"])
		}
		if fields["target"] != nil {
			target, err = stackString(fields["target"])
		}
	}
	if err != nil || context == "" || len(context) > 512 || strings.HasPrefix(context, "/") || strings.ContainsAny(context, "\\:$") || strings.ContainsFunc(context, unicode.IsControl) {
		return nil, errors.New("invalid Git Compose build context")
	}
	resolved := path.Clean(path.Join(settings.ContextDir, path.Dir(settings.ComposeFile), context))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || resolved != "." && !fs.ValidPath(resolved) {
		return nil, errors.New("git Compose context escapes checkout")
	}
	if resolved == "." {
		resolved = ""
	}
	dockerfile = path.Clean(dockerfile)
	if dockerfile == "." || !fs.ValidPath(dockerfile) || strings.ContainsAny(dockerfile, "\\:$") {
		return nil, errors.New("invalid Git Compose Dockerfile")
	}
	if ValidateHostingBuildSettings(HostingBuildSettings{Strategy: "dockerfile", ContextDir: resolved, DockerfilePath: dockerfile, BuildTarget: target}) != nil {
		return nil, errors.New("invalid Git Compose build settings")
	}
	args, err := parseHostingComposeBuildArgs(argsNode, settings.BuildVariables)
	if err != nil {
		return nil, err
	}
	for _, secret := range settings.BuildSecrets {
		if _, collision := args[secret.Name]; collision {
			return nil, errors.New("compose public argument collides with build secret")
		}
	}
	return &HostingStackBuild{Context: resolved, Dockerfile: dockerfile, Target: target, Args: args}, nil
}

// HostingStackBuildOwner resolves a build service or unique same-image alias.
func HostingStackBuildOwner(definition HostingStackDefinition, component string) (string, error) {
	owners := map[string]string{}
	for _, service := range definition.Services {
		if service.Build != nil && service.Image != "" {
			if owners[service.Image] != "" {
				return "", errors.New("ambiguous Git Compose built image")
			}
			owners[service.Image] = service.Name
		}
	}
	for _, service := range definition.Services {
		if service.Name == component {
			if service.Build != nil {
				return component, nil
			}
			return owners[service.Image], nil
		}
	}
	return "", errors.New("unknown Git Compose component")
}

// HostingComposeBuildArtifact returns the owning artifact for a built component or alias.
func HostingComposeBuildArtifact(result HostingBuildComposeResult, component string) (HostingBuildArtifact, bool) {
	owner, err := HostingStackBuildOwner(result.Definition, component)
	if err != nil || owner == "" {
		return HostingBuildArtifact{}, false
	}
	for _, service := range result.Services {
		if service.Component == owner {
			return service.Artifact, true
		}
	}
	return HostingBuildArtifact{}, false
}

// ValidateHostingBuildComposeResult checks complete source, component, daemon and immutable artifact binding.
func ValidateHostingBuildComposeResult(result HostingBuildComposeResult, build HostingBuild) error {
	expected := result
	expected.Hash = ""
	if result.Services == nil || build.SourceReceipt == nil || result.SourceReceiptHash != HostingDocumentHash(build.SourceReceipt) || result.Hash != HostingDocumentHash(expected) {
		return errors.New("git Compose result binding changed")
	}
	definition, err := ParseHostingGitCompose(result.Source, build.Snapshot.Source.Settings)
	if err != nil || HostingDocumentHash(definition) != HostingDocumentHash(result.Definition) {
		return errors.New("git Compose definition changed")
	}
	count := 0
	for _, service := range definition.Services {
		if service.Build == nil {
			continue
		}
		if count >= len(result.Services) || result.Services[count].Component != service.Name {
			return errors.New("git Compose build coverage changed")
		}
		child := build
		child.ID = HostingComposeBuildID(build.ID, service.Name)
		if ValidateHostingBuildArtifact(result.Services[count].Artifact, child) != nil {
			return errors.New("git Compose artifact changed")
		}
		count++
	}
	if count != len(result.Services) {
		return errors.New("git Compose artifact set changed")
	}
	return nil
}

// HostingGitComposeRef selects one successful prepared result without converting the Git resource.
type HostingGitComposeRef struct {
	BuildID          string `json:"build_id"`
	SourceGeneration int64  `json:"source_generation"`
	ResultHash       string `json:"result_hash"`
}
