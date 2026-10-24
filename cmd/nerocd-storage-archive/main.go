// Command nerocd-storage-archive is an internal-use confined upload archive
// helper. It does not authorize, coordinate or publish backups/restores.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"nerocd/internal/storagearchive"
)

func main() { os.Exit(command()) }

func command() int { return commandReady(nil) }

func commandReady(ready func()) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	joined := make(chan struct{})
	interrupt := context.AfterFunc(ctx, func() {
		// This process owns these streams. Closing them interrupts blocked pipe
		// IO; package APIs do not promise this for arbitrary caller streams.
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		close(joined)
	})
	defer func() {
		if !interrupt() {
			<-joined
		}
	}()
	if ready != nil {
		ready()
	}
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.OpenRoot, "/tmp"); err != nil {
		// Upload names, contents and parser/OS errors never enter diagnostics.
		_, _ = fmt.Fprintln(os.Stderr, "storage archive helper failed")
		return 1
	}
	return 0
}

type contextInput struct {
	ctx context.Context
	in  io.Reader
}

func (r contextInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer, openRoot func(string) (*os.Root, error), spoolDir string) (err error) {
	if len(args) == 0 || (args[0] != "create" && args[0] != "validate" && args[0] != "restore") {
		return errors.New("invalid archive helper mode")
	}
	l := storagearchive.DefaultLimits()
	flags := flag.NewFlagSet("storage-archive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Int64Var(&l.ArchiveBytes, "max-archive-bytes", l.ArchiveBytes, "physical archive byte budget")
	flags.Int64Var(&l.DataBytes, "max-data-bytes", l.DataBytes, "payload byte budget")
	flags.Int64Var(&l.FileBytes, "max-file-bytes", l.FileBytes, "individual file byte budget")
	flags.IntVar(&l.Members, "max-members", l.Members, "member budget including root")
	flags.IntVar(&l.Depth, "max-depth", l.Depth, "path component budget")
	flags.IntVar(&l.PathBytes, "max-path-bytes", l.PathBytes, "canonical path byte budget")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid archive helper options")
	}
	if args[0] == "create" {
		root, err := openRoot("/source")
		if err != nil {
			return fmt.Errorf("open source mount: %w", err)
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		_, err = storagearchive.Write(ctx, root, out, l)
		return err
	}
	spool, err := os.CreateTemp(spoolDir, "nerocd-storage-archive-")
	if err != nil {
		return fmt.Errorf("create private archive spool: %w", err)
	}
	defer func() {
		err = errors.Join(err, spool.Close(), os.Remove(spool.Name()))
	}()
	if err := spool.Chmod(0600); err != nil {
		return fmt.Errorf("protect archive spool: %w", err)
	}
	// Invalid limits are rejected before reading even one input byte.
	if err := l.Validate(); err != nil {
		return err
	}
	n, err := io.CopyBuffer(spool, io.LimitReader(contextInput{ctx, in}, l.ArchiveBytes+1), make([]byte, 32<<10))
	if err != nil {
		return fmt.Errorf("spool archive input: %w", err)
	}
	if n > l.ArchiveBytes {
		return storagearchive.ErrLimit
	}
	if err := spool.Sync(); err != nil {
		return fmt.Errorf("sync archive spool: %w", err)
	}
	var summary storagearchive.Summary
	if args[0] == "validate" {
		summary, err = storagearchive.Validate(ctx, spool, l)
	} else {
		root, openErr := openRoot("/target")
		if openErr != nil {
			return fmt.Errorf("open target mount: %w", openErr)
		}
		defer func() { err = errors.Join(err, root.Close()) }()
		summary, err = storagearchive.Extract(ctx, spool, root, l)
	}
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(summary); err != nil {
		return fmt.Errorf("write archive summary: %w", err)
	}
	return nil
}
