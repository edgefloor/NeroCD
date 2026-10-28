package hostingbuild

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// dockerfileSecretMounts follows the pinned PHP behavior: automatic env mounts
// apply to real RUN instructions unless a RUN already declares a secret mount.
// The pinned BuildKit parser distinguishes instructions from heredoc body text.
func dockerfileSecretMounts(ctx context.Context, original []byte, refs []SecretReference, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := parser.Parse(contextReader{ctx: ctx, reader: bytes.NewReader(original)})
	if err != nil {
		return nil, errors.New("invalid Dockerfile for build secrets")
	}
	lines := map[int]bool{}
	for _, node := range parsed.AST.Children {
		if !strings.EqualFold(node.Value, "run") {
			continue
		}
		explicit := false
		for _, flag := range node.Flags {
			if !strings.HasPrefix(flag, "--mount=") {
				continue
			}
			for _, field := range strings.Split(strings.TrimPrefix(flag, "--mount="), ",") {
				explicit = explicit || field == "type=secret"
			}
		}
		if !explicit {
			lines[node.StartLine] = true
		}
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	slices.Sort(names)
	var mounts strings.Builder
	for _, name := range names {
		mounts.WriteString(" --mount=type=secret,id=" + name + ",env=" + name)
	}
	var output bytes.Buffer
	for i, line := range bytes.SplitAfter(original, []byte("\n")) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if lines[i+1] {
			trimmed := bytes.TrimLeftFunc(line, unicode.IsSpace)
			prefix := len(line) - len(trimmed) + len("RUN")
			if prefix > len(line) {
				return nil, errors.New("dockerfile RUN location changed")
			}
			output.Write(line[:prefix])
			output.WriteString(mounts.String())
			output.Write(line[prefix:])
		} else {
			output.Write(line)
		}
		if int64(output.Len()) > limit {
			return nil, errors.New("secret-mounted Dockerfile exceeds budget")
		}
	}
	return output.Bytes(), nil
}
