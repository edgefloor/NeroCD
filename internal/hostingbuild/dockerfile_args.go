package hostingbuild

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// injectDockerfileBuildArgs changes only the private staged copy. The context
// digest continues to identify original input; the request binds this transform.
func injectDockerfileBuildArgs(ctx context.Context, r Request, source string) error {
	return transformPreparedDockerfile(ctx, r, source, func(ctx context.Context, raw []byte, limit int64) ([]byte, error) {
		return dockerfileArgumentDeclarations(ctx, raw, r.BuildArgs, limit)
	})
}

// transformPreparedDockerfile applies a bounded transform to the private staged
// regular file. Original source identity and source checkout remain unchanged.
func transformPreparedDockerfile(ctx context.Context, r Request, source string, transform func(context.Context, []byte, int64) ([]byte, error)) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := r.DockerfilePath
	if name == "" {
		name = "Dockerfile"
	}
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("prepared Dockerfile must be a regular file")
	}
	file, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() {
		return errors.New("prepared Dockerfile identity changed")
	}
	limit := min(r.Limits.ContextBytes, r.Limits.PlanBytes)
	if limit < 1 || info.Size() > limit {
		return errors.New("dockerfile exceeds injection budget")
	}
	original, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: file}, limit+1))
	if err != nil {
		return err
	}
	if int64(len(original)) > limit {
		return errors.New("dockerfile exceeds injection budget")
	}
	transformed, err := transform(ctx, original, limit)
	if err != nil {
		return err
	}
	total, err := preparedContextBytes(ctx, root, r.Limits.ContextBytes)
	if err != nil {
		return err
	}
	if int64(len(transformed)-len(original)) > r.Limits.ContextBytes-total {
		return errors.New("injected Dockerfile exceeds context budget")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = file.Truncate(0); err != nil {
		return err
	}
	if _, err = io.Copy(file, contextReader{ctx: ctx, reader: bytes.NewReader(transformed)}); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	return ctx.Err()
}

func preparedContextBytes(ctx context.Context, root *os.Root, limit int64) (int64, error) {
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular staged context entry")
		}
		if info.Size() > limit-total {
			return errors.New("prepared context exceeds budget")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func dockerfileArgumentDeclarations(ctx context.Context, original []byte, args map[string]string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := parser.Parse(contextReader{ctx: ctx, reader: bytes.NewReader(original)})
	if err != nil {
		return nil, errors.New("invalid Dockerfile for argument injection")
	}
	ends := map[int]bool{}
	for _, node := range parsed.AST.Children {
		if strings.EqualFold(node.Value, "from") {
			ends[node.EndLine] = true
		}
	}
	if len(ends) == 0 {
		return nil, errors.New("dockerfile has no build stage")
	}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	slices.Sort(names)
	var output bytes.Buffer
	lines := bytes.SplitAfter(original, []byte("\n"))
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(output.Len()+len(line)) > limit {
			return nil, errors.New("injected Dockerfile exceeds budget")
		}
		output.Write(line)
		if !ends[i+1] {
			continue
		}
		newline := "\n"
		if bytes.HasSuffix(line, []byte("\r\n")) {
			newline = "\r\n"
		}
		if !bytes.HasSuffix(line, []byte("\n")) {
			output.WriteString(newline)
		}
		for _, name := range names {
			declaration := "ARG " + name + newline
			if int64(output.Len()+len(declaration)) > limit {
				return nil, errors.New("injected Dockerfile exceeds budget")
			}
			output.WriteString(declaration)
		}
	}
	return output.Bytes(), nil
}
