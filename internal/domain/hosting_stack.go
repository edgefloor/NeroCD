package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// HostingStackVersion identifies the managed Compose subset, independently of Git Compose.
const HostingStackVersion = "managed-compose.v1"

// HostingStackSource preserves the exact submitted source, including reference expressions.
// Values are supplied separately by the existing protected runtime configuration contract.
type HostingStackSource struct {
	Original        string `json:"original"`
	PublicComponent string `json:"public_component"`
	ContainerPort   int    `json:"container_port"`
}

// HostingStackDefinition is a normalized, nonsecret execution document.
type HostingStackDefinition struct {
	Version         string                `json:"version"`
	OriginalHash    string                `json:"original_hash"`
	PublicComponent string                `json:"public_component"`
	ContainerPort   int                   `json:"container_port"`
	Services        []HostingStackService `json:"services"`
	Volumes         []string              `json:"volumes"`
}

// HostingStackService preserves service identity across source revisions.
type HostingStackService struct {
	Name        string                   `json:"name"`
	Build       *HostingStackBuild       `json:"build,omitempty"`
	Image       string                   `json:"image"`
	Command     []string                 `json:"command"`
	Environment map[string]string        `json:"environment,omitempty"`
	Mounts      []HostingStackMount      `json:"mounts,omitempty"`
	DependsOn   map[string]string        `json:"depends_on,omitempty"`
	Healthcheck *HostingStackHealthcheck `json:"healthcheck,omitempty"`
}

// HostingStackMount permits only stack-owned named volumes.
type HostingStackMount struct {
	Volume   string `json:"volume"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// HostingStackHealthcheck retains Compose container health semantics.
type HostingStackHealthcheck struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"start_period,omitempty"`
	Retries     int      `json:"retries"`
}

var stackName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// HostingStackValidationError contains a field path and fixed diagnostic, never field values.
type HostingStackValidationError struct{ location, reason string }

func (err *HostingStackValidationError) Error() string {
	return fmt.Sprintf("compose %s: %s", err.location, err.reason)
}
func stackError(location, reason string) error {
	return &HostingStackValidationError{location: location, reason: reason}
}

// ParseHostingStack fails before persistence on unknown, ambiguous, or unsafe fields.
// It never expands variables or contacts registries, Docker, or secret storage.
func ParseHostingStack(source HostingStackSource) (HostingStackDefinition, error) {
	return parseHostingStack(source, nil)
}

func parseHostingStack(source HostingStackSource, settings *HostingBuildSettings) (HostingStackDefinition, error) {
	var zero HostingStackDefinition
	if len(source.Original) == 0 || len(source.Original) > 128<<10 || source.ContainerPort < 1 || source.ContainerPort > 65535 {
		return zero, stackError("source", "requires bounded Compose text and a public container port")
	}
	decoder := yaml.NewDecoder(strings.NewReader(source.Original))
	var root yaml.Node
	if decoder.Decode(&root) != nil || len(root.Content) != 1 {
		return zero, stackError("source", "invalid YAML document")
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return zero, stackError("source", "requires exactly one document")
	}
	if err := stackYAML(root.Content[0], "root", 0); err != nil {
		return zero, err
	}
	top, err := stackFields(root.Content[0], "root", "services", "volumes")
	if err != nil {
		return zero, err
	}
	services, ok := top["services"]
	if !ok || services.Kind != yaml.MappingNode || len(services.Content) == 0 || len(services.Content) > 32 {
		return zero, stackError("services", "requires 1 to 16 named services")
	}
	sum := sha256.Sum256([]byte(source.Original))
	out := HostingStackDefinition{Version: HostingStackVersion, OriginalHash: "sha256:" + hex.EncodeToString(sum[:]), PublicComponent: source.PublicComponent, ContainerPort: source.ContainerPort, Volumes: []string{}}
	volumes := map[string]bool{}
	if node := top["volumes"]; node != nil {
		if node.Kind != yaml.MappingNode {
			return zero, stackError("volumes", "requires named volume mapping")
		}
		for i := 0; i < len(node.Content); i += 2 {
			name, value := node.Content[i].Value, node.Content[i+1]
			if !stackName.MatchString(name) || value.Tag != "!!null" && (value.Kind != yaml.MappingNode || len(value.Content) != 0) {
				return zero, stackError("volumes."+name, "only owned default-driver volumes are supported")
			}
			volumes[name] = true
		}
	}
	publicFound := false
	for i := 0; i < len(services.Content); i += 2 {
		name := services.Content[i].Value
		if !stackName.MatchString(name) {
			return zero, stackError("services", "invalid service name")
		}
		service, err := parseStackService(name, services.Content[i+1], volumes, settings)
		if err != nil {
			return zero, err
		}
		out.Services = append(out.Services, service)
		publicFound = publicFound || name == source.PublicComponent
	}
	if len(volumes) > 32 {
		return zero, stackError("volumes", "supports at most 32 owned named volumes")
	}
	if !publicFound {
		return zero, stackError("public_component", "must select an existing service")
	}
	for volume := range volumes {
		out.Volumes = append(out.Volumes, volume)
	}
	sort.Strings(out.Volumes)
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	state := map[string]int{}
	byName := map[string]HostingStackService{}
	for _, service := range out.Services {
		byName[service.Name] = service
	}
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			return stackError("services."+name+".depends_on", "dependency cycle")
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for dependency, condition := range byName[name].DependsOn {
			target, ok := byName[dependency]
			if dependency == source.PublicComponent && condition == "service_completed_successfully" {
				return stackError("public_component", "cannot select a successful init job")
			}
			if !ok {
				return stackError("services."+name+".depends_on", "unknown dependency")
			}
			if condition == "service_healthy" && target.Healthcheck == nil {
				return stackError("services."+name+".depends_on", "healthy dependency requires a healthcheck")
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, service := range out.Services {
		if err := visit(service.Name); err != nil {
			return zero, err
		}
	}
	return out, nil
}

// stackYAML rejects aliases, duplicate keys and excess nesting before typed decoding.
func stackYAML(node *yaml.Node, location string, depth int) error {
	if depth > 24 || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return stackError(location, "aliases, anchors, or excessive nesting are unsupported")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return stackError(location, "duplicate or non-string mapping key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := stackYAML(child, location, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func stackFields(node *yaml.Node, location string, allowed ...string) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, stackError(location, "requires mapping")
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		name := node.Content[i].Value
		supported := false
		for _, key := range allowed {
			supported = supported || key == name
		}
		if !supported {
			return nil, stackError(location+"."+name, "unsupported field in managed Compose v1")
		}
		fields[name] = node.Content[i+1]
	}
	return fields, nil
}
func stackString(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || strings.ContainsRune(node.Value, 0) {
		return "", errors.New("requires string")
	}
	return node.Value, nil
}
func stackStrings(node *yaml.Node) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > 128 {
		return nil, errors.New("requires bounded string list")
	}
	values := make([]string, 0, len(node.Content))
	for _, child := range node.Content {
		value, err := stackString(child)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}
func parseStackService(name string, node *yaml.Node, volumes map[string]bool, settings *HostingBuildSettings) (HostingStackService, error) {
	var zero HostingStackService
	location := "services." + name
	allowed := []string{"image", "environment", "command", "depends_on", "volumes", "healthcheck"}
	if settings != nil {
		allowed = append(allowed, "build")
	}
	fields, err := stackFields(node, location, allowed...)
	if err != nil {
		return zero, err
	}
	var build *HostingStackBuild
	if fields["build"] != nil {
		if settings == nil {
			return zero, stackError(location+".build", "Git Compose required")
		}
		build, err = parseHostingStackBuild(fields["build"], *settings)
		if err != nil {
			return zero, err
		}
	}
	image := ""
	if fields["image"] != nil {
		image, err = stackString(fields["image"])
		if err != nil || strings.Contains(image, "$") {
			return zero, stackError(location+".image", "requires literal image reference")
		}
		if !strings.Contains(image, "@") && strings.LastIndex(image, ":") <= strings.LastIndex(image, "/") {
			image += ":latest"
		}
		image, err = NormalizeHostingImage(image)
		if err != nil {
			return zero, stackError(location+".image", "invalid image reference")
		}
	} else if build == nil {
		return zero, stackError(location+".image", "requires image or Git Compose build")
	}
	out := HostingStackService{Name: name, Image: image, Build: build}
	if command := fields["command"]; command != nil {
		out.Command, err = stackStrings(command)
		if err == nil {
			for _, value := range out.Command {
				if _, err = ExpandHostingStackValue(value, map[string]string{}, false); err != nil {
					break
				}
			}
		}
		if err != nil {
			return zero, stackError(location+".command", "use exec-form string list")
		}
	}
	if environment := fields["environment"]; environment != nil {
		out.Environment = map[string]string{}
		add := func(key, value string) error {
			if !runtimeEnvironmentName.MatchString(key) || strings.HasPrefix(key, "NEROCD_") {
				return stackError(location+".environment", "invalid environment name")
			}
			if _, ok := out.Environment[key]; ok {
				return stackError(location+".environment", "duplicate environment name")
			}
			if len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
				return stackError(location+".environment", "invalid environment expression")
			}
			if _, err := ExpandHostingStackValue(value, map[string]string{}, false); err != nil {
				return stackError(location+".environment."+key, "unsupported interpolation")
			}
			out.Environment[key] = value
			return nil
		}
		switch environment.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(environment.Content); i += 2 {
				key := environment.Content[i].Value
				valueNode := environment.Content[i+1]
				value := valueNode.Value
				if valueNode.Tag == "!!null" {
					value = "${" + key + "?required}"
				} else if valueNode.Kind != yaml.ScalarNode || (valueNode.Tag != "!!str" && valueNode.Tag != "!!bool" && valueNode.Tag != "!!int" && valueNode.Tag != "!!float") {
					return zero, stackError(location+".environment", "requires scalar values")
				}
				if err = add(key, value); err != nil {
					return zero, err
				}
			}
		case yaml.SequenceNode:
			entries, e := stackStrings(environment)
			if e != nil {
				return zero, stackError(location+".environment", "requires strings")
			}
			for _, entry := range entries {
				key, value, found := strings.Cut(entry, "=")
				if !found {
					value = "${" + key + "?required}"
				}
				if err = add(key, value); err != nil {
					return zero, err
				}
			}
		default:
			return zero, stackError(location+".environment", "requires mapping or list")
		}
	}
	if mounts := fields["volumes"]; mounts != nil {
		entries, e := stackStrings(mounts)
		if e != nil {
			return zero, stackError(location+".volumes", "use named-volume short syntax")
		}
		targets := map[string]bool{}
		for _, entry := range entries {
			parts := strings.Split(entry, ":")
			if len(parts) < 2 || len(parts) > 3 || !stackName.MatchString(parts[0]) || !strings.HasPrefix(parts[1], "/") || ValidateHostingStorageMountPath(path.Clean(parts[1])) != nil || strings.Contains(parts[1], "$") || targets[path.Clean(parts[1])] || len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
				return zero, stackError(location+".volumes", "requires unique absolute targets and owned named volumes")
			}
			targets[path.Clean(parts[1])] = true
			volumes[parts[0]] = true
			out.Mounts = append(out.Mounts, HostingStackMount{Volume: parts[0], Target: path.Clean(parts[1]), ReadOnly: len(parts) == 3 && parts[2] == "ro"})
		}
	}
	if depends := fields["depends_on"]; depends != nil {
		out.DependsOn = map[string]string{}
		if depends.Kind == yaml.SequenceNode {
			names, e := stackStrings(depends)
			if e != nil {
				return zero, stackError(location+".depends_on", "requires names")
			}
			for _, dependency := range names {
				if _, exists := out.DependsOn[dependency]; exists {
					return zero, stackError(location+".depends_on", "duplicate dependency")
				}
				out.DependsOn[dependency] = "service_started"
			}
		} else {
			if depends.Kind != yaml.MappingNode {
				return zero, stackError(location+".depends_on", "requires mapping or list")
			}
			for i := 0; i < len(depends.Content); i += 2 {
				dependency := depends.Content[i].Value
				conditionFields, e := stackFields(depends.Content[i+1], location+".depends_on."+dependency, "condition")
				if e != nil {
					return zero, e
				}
				condition, e := stackString(conditionFields["condition"])
				if e != nil || condition != "service_started" && condition != "service_healthy" && condition != "service_completed_successfully" {
					return zero, stackError(location+".depends_on."+dependency, "unsupported condition")
				}
				out.DependsOn[dependency] = condition
			}
		}
	}
	if health := fields["healthcheck"]; health != nil {
		h, e := stackFields(health, location+".healthcheck", "test", "interval", "timeout", "start_period", "retries")
		if e != nil {
			return zero, e
		}
		test, e := stackStrings(h["test"])
		if e != nil || len(test) < 2 || test[0] != "CMD" && test[0] != "CMD-SHELL" || test[0] == "CMD-SHELL" && len(test) != 2 {
			return zero, stackError(location+".healthcheck.test", "requires CMD or CMD-SHELL list")
		}
		for _, value := range test {
			if _, e := ExpandHostingStackValue(value, map[string]string{}, false); e != nil {
				return zero, stackError(location+".healthcheck.test", "unsupported interpolation")
			}
		}
		check := HostingStackHealthcheck{Test: test, Retries: 3}
		for _, duration := range []struct {
			key  string
			dest *string
		}{{"interval", &check.Interval}, {"timeout", &check.Timeout}, {"start_period", &check.StartPeriod}} {
			if value := h[duration.key]; value != nil {
				raw, e := stackString(value)
				d, parseErr := time.ParseDuration(raw)
				if e != nil || parseErr != nil || d < time.Millisecond || d > 10*time.Minute {
					return zero, stackError(location+".healthcheck."+duration.key, "requires duration between 1ms and 10m")
				}
				*duration.dest = raw
			}
		}
		if retries := h["retries"]; retries != nil {
			if retries.Tag != "!!int" || retries.Decode(&check.Retries) != nil || check.Retries < 1 || check.Retries > 100 {
				return zero, stackError(location+".healthcheck.retries", "requires integer from 1 to 100")
			}
		}
		out.Healthcheck = &check
	}
	return out, nil
}

// ExpandHostingStackValue implements the supported Compose interpolation operators.
// Errors name the variable only and never echo source literals or protected values.
// Validation mode checks syntax while permitting missing values.
func ExpandHostingStackValue(expression string, values map[string]string, require bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(expression); {
		if expression[i] != '$' {
			out.WriteByte(expression[i])
			i++
			continue
		}
		i++
		if i < len(expression) && expression[i] == '$' {
			out.WriteByte('$')
			i++
			continue
		}
		braced := i < len(expression) && expression[i] == '{'
		if braced {
			i++
		}
		start := i
		for i < len(expression) && (expression[i] == '_' || expression[i] >= 'A' && expression[i] <= 'Z' || expression[i] >= 'a' && expression[i] <= 'z' || i > start && expression[i] >= '0' && expression[i] <= '9') {
			i++
		}
		name := expression[start:i]
		if name == "" {
			return "", errors.New("invalid Compose interpolation")
		}
		operator, operand := "", ""
		if braced {
			if i >= len(expression) {
				return "", errors.New("unterminated Compose interpolation")
			}
			if expression[i] != '}' {
				if expression[i] == ':' {
					operator = ":"
					i++
				}
				if i >= len(expression) || !strings.ContainsRune("-?+", rune(expression[i])) {
					return "", errors.New("unsupported Compose interpolation")
				}
				operator += string(expression[i])
				i++
				end := strings.IndexByte(expression[i:], '}')
				if end < 0 {
					return "", errors.New("unterminated Compose interpolation")
				}
				operand = expression[i : i+end]
				i += end
				if strings.ContainsAny(operand, "${}") {
					return "", errors.New("nested Compose interpolation is unsupported")
				}
			}
			i++
		}
		value, set := values[name]
		populated := set && value != ""
		switch operator {
		case "-":
			if !set {
				value = operand
			}
		case ":-":
			if !populated {
				value = operand
			}
		case "+":
			value = ""
			if set {
				value = operand
			}
		case ":+":
			value = ""
			if populated {
				value = operand
			}
		case "?":
			if !set && require {
				return "", fmt.Errorf("compose variable %s is required", name)
			}
		case ":?":
			if !populated && require {
				return "", fmt.Errorf("compose variable %s is required and nonempty", name)
			}
		}
		out.WriteString(value)
	}
	if out.Len() > 4096 {
		return "", errors.New("expanded Compose value exceeds limit")
	}
	return out.String(), nil
}

// HostingStackObjectID derives stable, destination-bound runtime object names.
func HostingStackObjectID(binding, kind, name string) string {
	sum := sha256.Sum256([]byte(binding + "\x00" + kind + "\x00" + name))
	return "ncd_" + hex.EncodeToString(sum[:16])
}

// Source JSON has its own closed key vocabulary, separate from runtime entries.
func uniqueStackSourceJSON(raw []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 4 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delimiter, object := token.(json.Delim)
		if !object {
			return true
		}
		if delimiter != '{' {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			switch key {
			case "kind", "compose", "original", "public_component", "container_port":
			default:
				return false
			}
			seen[key] = true
			if !walk(depth + 1) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim('}')
	}
	if !walk(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
