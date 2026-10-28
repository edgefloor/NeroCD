package main

import (
	"encoding/hex"
	"encoding/json"
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
	outputDirectory, err := openSecureBackupDirectory(*output)
	if err != nil {
		return errors.New("backup export destination must already be owner-only")
	}
	defer func() { _ = outputDirectory.Close() }()
	manifest, dump, err := openVerifiedBackupArchive(*input)
	if err != nil {
		return err
	}
	defer func() { _ = dump.Close() }()
	random, err := randomRuntimeHex(6)
	if err != nil {
		return errors.New("backup export name could not be created")
	}
	name := "export-" + time.Now().UTC().Format("20060102T150405Z") + "-" + random
	stagingName := "." + name
	staging, err := createBackupDirectoryAt(outputDirectory, stagingName)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			removeBackupStagingAt(outputDirectory, staging, stagingName)
		}
		_ = staging.Close()
	}()
	outputFile, err := createBackupFileAt(staging, "database.dump")
	if err != nil {
		return err
	}
	defer func() { _ = outputFile.Close() }()
	if _, err := io.Copy(outputFile, dump); err != nil {
		return errors.New("backup export could not copy verified archive")
	}
	if err := outputFile.Sync(); err != nil {
		return err
	}
	copied, err := checksumOpenBackupFile(outputFile, "database.dump")
	if err != nil || copied != manifest.Files[0] {
		return errors.New("backup export verification failed")
	}
	if err := writeBackupManifestAt(staging, manifest); err != nil {
		return err
	}
	final := filepath.Join(*output, name)
	if err := publishBackupDirectoryAt(outputDirectory, stagingName, name); err != nil {
		return errors.New("backup export directory could not be durably published")
	}
	published = true
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
	manifest, dump, err := openVerifiedBackupArchive(input)
	if err != nil {
		return backupManifest{}, err
	}
	return manifest, dump.Close()
}

// openVerifiedBackupArchive keeps the exact verified dump open for the caller.
// All archive reads use the admitted directory, including after ancestor moves.
func openVerifiedBackupArchive(input string) (backupManifest, *os.File, error) {
	directory, err := openSecureBackupDirectory(input)
	if err != nil {
		return backupManifest{}, nil, errors.New("backup input directory must be owner-only")
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return backupManifest{}, nil, errors.New("backup input directory cannot be read")
	}
	if len(entries) != 2 {
		return backupManifest{}, nil, errors.New("backup archive contains unexpected entries")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if (entry.Name() != "manifest.json" && entry.Name() != "database.dump") || entry.Type()&os.ModeSymlink != 0 {
			return backupManifest{}, nil, errors.New("backup archive contains unexpected entries")
		}
		seen[entry.Name()] = true
	}
	if !seen["manifest.json"] || !seen["database.dump"] {
		return backupManifest{}, nil, errors.New("backup archive is incomplete")
	}
	metadata, err := openSecureBackupFileAt(directory, "manifest.json")
	if err != nil {
		return backupManifest{}, nil, errors.New("backup manifest cannot be read")
	}
	raw, err := io.ReadAll(io.LimitReader(metadata, maxBackupMetadataBytes+1))
	_ = metadata.Close()
	if err != nil {
		return backupManifest{}, nil, errors.New("backup manifest cannot be read")
	}
	manifest, err := decodeBackupManifest(raw)
	if err != nil || !validBackupManifest(manifest) {
		return backupManifest{}, nil, errors.New("backup manifest is invalid or incompatible")
	}
	dump, err := openSecureBackupFileAt(directory, "database.dump")
	if err != nil {
		return backupManifest{}, nil, errors.New("backup checksum verification failed")
	}
	checksum, err := checksumOpenBackupFile(dump, "database.dump")
	if err != nil || checksum != manifest.Files[0] {
		_ = dump.Close()
		return backupManifest{}, nil, errors.New("backup checksum verification failed")
	}
	if _, err := dump.Seek(0, io.SeekStart); err != nil {
		_ = dump.Close()
		return backupManifest{}, nil, err
	}
	return manifest, dump, nil
}

func writeBackupManifestAt(directory *os.File, manifest backupManifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	file, err := createBackupFileAt(directory, "manifest.json")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return directory.Sync()
}

func validBackupManifest(manifest backupManifest) bool {
	if manifest.Version != backupManifestVersion || manifest.CreatedAt.IsZero() || strings.TrimSpace(manifest.ApplicationVersion) == "" || strings.TrimSpace(manifest.SchemaIdentity) == "" || strings.TrimSpace(manifest.Database) == "" || len(manifest.Files) != 1 || manifest.Files[0].Path != "database.dump" || len(manifest.Files[0].SHA256) != 64 || manifest.Files[0].Bytes < 0 {
		return false
	}
	_, err := hex.DecodeString(manifest.Files[0].SHA256)
	return err == nil
}
