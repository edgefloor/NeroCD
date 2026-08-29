package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupExport copies one verified archive to an operator-mounted destination.
// It intentionally knows nothing about remote storage vendors: transport,
// encryption, and retention belong to the operator after this atomic handoff.
func backupExport(args []string) error {
	fs := flag.NewFlagSet("backup-export", flag.ContinueOnError)
	input := fs.String("input-dir", "", "private backup directory to verify and export")
	output := fs.String("output-dir", "", "existing owner-only off-host export destination")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" || strings.TrimSpace(*output) == "" {
		return errors.New("backup-export requires --input-dir and --output-dir")
	}
	if err := ensureSecureBackupParent(*output); err != nil {
		return errors.New("backup export destination must already be owner-only")
	}
	if _, err := verifyBackupArchive(*input); err != nil {
		return err
	}
	random, err := randomRuntimeHex(6)
	if err != nil {
		return errors.New("backup export name could not be created")
	}
	name := "export-" + time.Now().UTC().Format("20060102T150405Z") + "-" + random
	staging, err := os.MkdirTemp(*output, "."+name+"-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := os.Chmod(staging, 0700); err != nil {
		return errors.New("backup export staging permissions could not be secured")
	}
	for _, file := range []string{"database.dump", "manifest.json"} {
		if err := copySecureBackupFile(filepath.Join(*input, file), filepath.Join(staging, file)); err != nil {
			return errors.New("backup export could not copy verified archive")
		}
	}
	if _, err := verifyBackupArchive(staging); err != nil {
		return errors.New("backup export verification failed")
	}
	final := filepath.Join(*output, name)
	if _, err := os.Lstat(final); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup export destination already exists")
	}
	if err := os.Rename(staging, final); err != nil {
		return errors.New("backup export could not be published")
	}
	if err := syncBackupDirectory(*output); err != nil {
		return errors.New("backup export directory could not be durably published")
	}
	fmt.Println(final)
	return nil
}

func verifyBackup(args []string) error {
	fs := flag.NewFlagSet("backup-verify", flag.ContinueOnError)
	input := fs.String("input-dir", "", "private backup directory to verify")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" {
		return errors.New("backup-verify requires --input-dir")
	}
	if _, err := verifyBackupArchive(*input); err != nil {
		return err
	}
	fmt.Println("backup verified")
	return nil
}

// verifyBackupArchive admits exactly the two files NeroCD writes. It is used
// before an archive is copied off host and before it is accepted for restore.
func verifyBackupArchive(input string) (backupManifest, error) {
	if err := ensureSecureBackupParent(input); err != nil {
		return backupManifest{}, errors.New("backup input directory must be owner-only")
	}
	entries, err := os.ReadDir(input)
	if err != nil {
		return backupManifest{}, errors.New("backup input directory cannot be read")
	}
	if len(entries) != 2 {
		return backupManifest{}, errors.New("backup archive contains unexpected entries")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if (entry.Name() != "manifest.json" && entry.Name() != "database.dump") || entry.Type()&os.ModeSymlink != 0 {
			return backupManifest{}, errors.New("backup archive contains unexpected entries")
		}
		seen[entry.Name()] = true
	}
	if !seen["manifest.json"] || !seen["database.dump"] {
		return backupManifest{}, errors.New("backup archive is incomplete")
	}
	manifestPath := filepath.Join(input, "manifest.json")
	if err := ensureSecureBackupFile(manifestPath); err != nil {
		return backupManifest{}, errors.New("backup manifest cannot be read")
	}
	raw, err := readSecureBackupFile(manifestPath)
	if err != nil {
		return backupManifest{}, errors.New("backup manifest cannot be read")
	}
	manifest, err := decodeBackupManifest(raw)
	if err != nil || !validBackupManifest(manifest) {
		return backupManifest{}, errors.New("backup manifest is invalid or incompatible")
	}
	dump, err := checksumBackupFile(filepath.Join(input, "database.dump"))
	if err != nil || dump.SHA256 != manifest.Files[0].SHA256 || dump.Bytes != manifest.Files[0].Bytes {
		return backupManifest{}, errors.New("backup checksum verification failed")
	}
	return manifest, nil
}

func validBackupManifest(manifest backupManifest) bool {
	if manifest.Version != backupManifestVersion || manifest.CreatedAt.IsZero() || strings.TrimSpace(manifest.ApplicationVersion) == "" || strings.TrimSpace(manifest.SchemaIdentity) == "" || strings.TrimSpace(manifest.Database) == "" || len(manifest.Files) != 1 || manifest.Files[0].Path != "database.dump" || len(manifest.Files[0].SHA256) != 64 || manifest.Files[0].Bytes < 0 {
		return false
	}
	_, err := hex.DecodeString(manifest.Files[0].SHA256)
	return err == nil
}

func copySecureBackupFile(source, destination string) error {
	input, err := openSecureBackupFile(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
