package runnerruntime

import (
	"flag"
	"os"
	"strings"
	"time"
)

func runRunner(args []string) error {
	fs := flag.NewFlagSet("runner-test", flag.ContinueOnError)
	server := fs.String("server", "", "")
	credentialFile := fs.String("credential-file", "", "")
	journalDir := fs.String("journal-dir", "", "")
	once := fs.Bool("once", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	credential, err := os.ReadFile(*credentialFile)
	if err != nil {
		return err
	}
	return Run(Config{Server: *server, Token: strings.TrimSpace(string(credential)), RunnerID: "runner_test", WorkDir: os.TempDir(), JournalDir: *journalDir, Execute: true, Once: *once, PollInterval: time.Millisecond, CancelPollInterval: time.Millisecond})
}
