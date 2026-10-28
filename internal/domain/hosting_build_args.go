package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// HostingBuildVariable is a public build-time value. ARG values can be retained
// in image metadata and output; managed secrets use a separate delivery contract.
type HostingBuildVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func validHostingBuildArgName(name string) bool {
	if !runtimeEnvironmentName.MatchString(name) {
		return false
	}
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"NEROCD_", "BUILDKIT_", "BUILDX_"} {
		if strings.HasPrefix(upper, prefix) {
			return false
		}
	}
	switch upper {
	case "HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY", "BUILDPLATFORM", "BUILDOS", "BUILDARCH", "BUILDVARIANT", "TARGETPLATFORM", "TARGETOS", "TARGETARCH", "TARGETVARIANT":
		return false
	}
	return true
}

func validHostingBuildArgValue(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validateHostingBuildVariables(variables []HostingBuildVariable) error {
	if len(variables) > 64 {
		return errors.New("too many build variables")
	}
	names := map[string]bool{}
	for _, variable := range variables {
		if !validHostingBuildArgName(variable.Name) || names[variable.Name] || !validHostingBuildArgValue(variable.Value) {
			return errors.New("invalid or duplicate public build variable")
		}
		names[variable.Name] = true
	}
	return nil
}

func parseHostingComposeBuildArgs(node *yaml.Node, variables []HostingBuildVariable) (map[string]string, error) {
	values := map[string]string{}
	for _, variable := range variables {
		values[variable.Name] = variable.Value
	}
	args := map[string]string{}
	names := map[string]bool{}
	add := func(name string, expression *string) error {
		if !validHostingBuildArgName(name) || names[name] || len(names) >= 64 {
			return errors.New("invalid or duplicate Compose build argument")
		}
		names[name] = true
		if expression == nil {
			if value, set := values[name]; set {
				args[name] = value
			}
			return nil
		}
		value, err := ExpandHostingStackValue(*expression, values, true)
		if err != nil {
			return err
		}
		if !validHostingBuildArgValue(value) {
			return errors.New("invalid Compose build argument value")
		}
		args[name] = value
		return nil
	}
	if node != nil && node.Tag != "!!null" {
		switch node.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				key, err := stackString(node.Content[i])
				if err != nil {
					return nil, err
				}
				val := node.Content[i+1]
				if val.Tag == "!!null" {
					if err = add(key, nil); err != nil {
						return nil, err
					}
					continue
				}
				value, err := hostingComposeBuildArgScalar(val)
				if err != nil {
					return nil, err
				}
				if err = add(key, &value); err != nil {
					return nil, err
				}
			}
		case yaml.SequenceNode:
			for _, entry := range node.Content {
				value, err := stackString(entry)
				if err != nil {
					return nil, err
				}
				name, expression, set := strings.Cut(value, "=")
				var expr *string
				if set {
					expr = &expression
				}
				if err = add(name, expr); err != nil {
					return nil, err
				}
			}
		default:
			return nil, errors.New("Compose build args require a map or list")
		}
	}
	// Configured build-time variables are CLI overrides, including variables not
	// explicitly listed by a service. They never come from the host environment.
	for name, value := range values {
		args[name] = value
	}
	if len(args) > 64 {
		return nil, errors.New("too many Compose build arguments")
	}
	if len(args) == 0 {
		return nil, nil
	}
	return args, nil
}

// Compose accepts scalar map values and normalizes numbers and booleans to text.
func hostingComposeBuildArgScalar(node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", errors.New("Compose build argument requires a scalar")
	}
	switch node.Tag {
	case "!!str":
		return stackString(node)
	case "!!int", "!!float", "!!bool":
		var value any
		if node.Decode(&value) != nil {
			return "", errors.New("invalid Compose build argument scalar")
		}
		if number, ok := value.(float64); ok && (math.IsNaN(number) || math.IsInf(number, 0)) {
			return "", errors.New("nonfinite Compose build argument")
		}
		return fmt.Sprint(value), nil
	default:
		return "", errors.New("unsupported Compose build argument scalar")
	}
}
