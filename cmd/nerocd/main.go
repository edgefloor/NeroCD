package main

import (
	"fmt"
	"os"
)

// version is set by the reproducible release build with -ldflags -X. The
// development default keeps local builds self-describing without embedding a
// release identity in source.
var version = "0.1.0-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "server":
		return runServer(args[1:])
	case "runner":
		return runRunner(args[1:])
	case "health":
		return callAPI(args[1:], "/api/v1/health")
	case "ready":
		return callReady(args[1:])
	case "projects":
		return callAPI(args[1:], "/api/v1/projects")
	case "runs":
		return callAPI(args[1:], "/api/v1/runs")
	case "templates":
		return callAPI(args[1:], "/api/v1/templates")
	case "run-logs":
		return callAPI(args[1:], "/api/v1/run-logs")
	case "run-log-retention":
		return runLogRetention(args[1:])
	case "session":
		return createSession(args[1:])
	case "migrate":
		return migrateDatabase(args[1:])
	case "backup":
		return backupDatabase(args[1:])
	case "backup-export":
		return backupExport(args[1:])
	case "backup-verify":
		return verifyBackup(args[1:])
	case "restore":
		return restoreDatabase(args[1:])
	case "backup-scheduler":
		return backupScheduler(args[1:])
	case "seed-dev":
		return seedDevelopmentDatabase(args[1:])
	case "bootstrap-admin":
		return bootstrapAdmin(args[1:])
	case "provision-app-role":
		return provisionAppRole(args[1:])
	case "doctor":
		return productionDoctor()
	case "smoke":
		return smoke(args[1:])
	case "contract":
		return validateContract(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() error {
	fmt.Println(`NeroCD

Usage:
  nerocd server [--addr :8080]
  nerocd runner [--server http://127.0.0.1:8080] (--token ADMIN_TOKEN | --credential-file /run/secrets/runner-token)
  nerocd health [--addr http://127.0.0.1:8080]
	  nerocd ready [--addr http://127.0.0.1:8080]
  nerocd projects [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd templates [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd runs [--addr http://127.0.0.1:8080] [--token ncd_...]
  nerocd run-logs [--addr http://127.0.0.1:8080] [--token ncd_...]
	  nerocd run-log-retention <status|preview|update|execute> [--addr http://127.0.0.1:8080] --token ncd_... [--enabled] [--keep-days 30] [--batch-size 1000] [--policy-version 1 --request-id stable-id]
	  nerocd session --email <email> --password <password> [--addr http://127.0.0.1:8080]
  nerocd migrate [--database-url postgres://...]
	  nerocd backup --database-url postgres://... --output-dir /secure/backups
	  nerocd backup-export --input-dir /secure/backups/backup-... --output-dir /mounted/off-host
	  nerocd backup-verify --input-dir /secure/backups/backup-...
	  nerocd restore --database-url postgres://... --input-dir /secure/backups/backup-...
	  nerocd backup-scheduler --output-dir /secure/backups [--runner-file-root /secure/runner-files] [--interval-seconds 86400] [--retention-count 7]
	  nerocd seed-dev [--database-url postgres://...]
	  nerocd bootstrap-admin --email admin@example.com --name 'Initial Admin' (--password-stdin | --password-file /secure/password)
	  nerocd doctor
  nerocd smoke [--addr http://127.0.0.1:8080]
  nerocd contract [--openapi openapi.yaml]
  nerocd version`)
	return nil
}
