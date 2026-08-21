package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionConfigFailsClosedAndReadsOwnerOnlySecret(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	t.Setenv("NEROCD_DATABASE_URL", "")
	t.Setenv("NEROCD_IMAGE_REF", "example.invalid/nerocd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("NEROCD_DATABASE_CREDENTIAL", "app")
	t.Setenv("NEROCD_APP_DATABASE_USER", "user")
	t.Setenv("NEROCD_PUBLIC_ORIGIN", "https://nerocd.example.invalid")
	if _, err := loadRuntimeConfig(":8080"); err == nil {
		t.Fatal("production accepted missing database secret")
	}
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte("postgres://user:secret@db:5432/nerocd?sslmode=disable\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_DATABASE_URL_FILE", path)
	if cfg, err := loadRuntimeConfig(":8080"); err != nil || cfg.databaseURL == "" || cfg.mode != modeProduction {
		t.Fatalf("production config=%#v err=%v", cfg, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRuntimeConfig(":8080"); err == nil {
		t.Fatal("production accepted group-readable secret")
	}
}

func TestProductionOriginAndCookiePolicyFailClosed(t *testing.T) {
	for _, origin := range []string{"http://nerocd.example.invalid", "https://nerocd.example.invalid/path", "https://user@nerocd.example.invalid", ""} {
		if err := validateProductionOrigin(origin); err == nil {
			t.Fatalf("accepted unsafe origin %q", origin)
		}
	}
	if err := validateProductionOrigin("https://nerocd.example.invalid"); err != nil {
		t.Fatalf("rejected exact HTTPS origin: %v", err)
	}
}

func TestTrustedProxyCIDRsFailClosed(t *testing.T) {
	for _, value := range []string{"not-a-cidr", "10.0.0.0/8,", "10.0.0.0/33"} {
		if err := validateTrustedProxyCIDRs(value); err == nil {
			t.Fatalf("accepted unsafe trusted proxy CIDRs %q", value)
		}
	}
	if err := validateTrustedProxyCIDRs("10.0.0.0/8, 2001:db8::/32"); err != nil {
		t.Fatalf("rejected valid trusted proxy CIDRs: %v", err)
	}
}

func TestProductionCredentialPairFailsClosedWithoutLeakingURLs(t *testing.T) {
	owner := "postgres://owner:owner-secret@db.example:5432/nerocd?sslmode=disable"
	app := "postgres://app:app-secret@db.example:5432/nerocd?sslmode=disable"
	if err := validateProductionCredentialPair(owner, app, "owner", "app"); err != nil {
		t.Fatalf("valid credential pair: %v", err)
	}
	for name, tc := range map[string]struct {
		ownerURL  string
		appURL    string
		ownerRole string
		appRole   string
	}{
		"equal_urls":              {owner, owner, "owner", "owner"},
		"same_user":               {owner, "postgres://owner:app-secret@db.example:5432/nerocd", "owner", "owner"},
		"same_password":           {owner, "postgres://app:owner-secret@db.example:5432/nerocd", "owner", "app"},
		"different_database":      {owner, "postgres://app:app-secret@db.example:5432/other", "owner", "app"},
		"different_host":          {owner, "postgres://app:app-secret@other.example:5432/nerocd", "owner", "app"},
		"malformed":               {owner, "not-a-url", "owner", "app"},
		"owner_role_mismatch":     {owner, app, "not-owner", "app"},
		"app_role_mismatch":       {owner, app, "owner", "not-app"},
		"missing_configured_role": {owner, app, "owner", ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateProductionCredentialPair(tc.ownerURL, tc.appURL, tc.ownerRole, tc.appRole)
			if err == nil {
				t.Fatal("accepted unsafe production credentials")
			}
			if strings.Contains(err.Error(), "owner-secret") || strings.Contains(err.Error(), "app-secret") || strings.Contains(err.Error(), "db.example") {
				t.Fatalf("credential error disclosed sensitive material: %v", err)
			}
		})
	}
}

func TestProductionDoctorValidatesBothStrictCredentialFiles(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	t.Setenv("NEROCD_IMAGE_REF", "example.invalid/nerocd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	t.Setenv("NEROCD_OWNER_DATABASE_USER", "owner")
	t.Setenv("NEROCD_APP_DATABASE_USER", "app")
	t.Setenv("NEROCD_PUBLIC_ORIGIN", "https://nerocd.example.invalid")
	dir := t.TempDir()
	ownerPath, appPath := filepath.Join(dir, "owner"), filepath.Join(dir, "app")
	owner := "postgres://owner:owner-secret@db.example:5432/nerocd?sslmode=disable\n"
	app := "postgres://app:app-secret@db.example:5432/nerocd?sslmode=disable\n"
	if err := os.WriteFile(ownerPath, []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appPath, []byte(app), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_OWNER_DATABASE_URL_FILE", ownerPath)
	t.Setenv("NEROCD_APP_DATABASE_URL_FILE", appPath)
	if err := productionDoctor(); err != nil {
		t.Fatalf("doctor rejected valid separate files: %v", err)
	}
	if err := os.WriteFile(appPath, []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	if err := productionDoctor(); err == nil {
		t.Fatal("doctor accepted equal owner/app credentials")
	} else if strings.Contains(err.Error(), "owner-secret") {
		t.Fatalf("doctor disclosed secret: %v", err)
	}
}

func TestProductionImageReferenceRejectsMutableForms(t *testing.T) {
	for _, value := range []string{
		"", "example.invalid/nerocd:latest", "example.invalid/nerocd:latest@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"example.invalid/nerocd@sha256:not-a-digest", "example.invalid/nerocd@sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if err := validateProductionImageReference(value); err == nil {
			t.Fatalf("accepted mutable/malformed image reference %q", value)
		}
	}
	if err := validateProductionImageReference("registry.example.invalid:5443/nerocd/server@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("rejected canonical digest: %v", err)
	}
}

func TestProductionMigrationRejectsSeedFlag(t *testing.T) {
	t.Setenv("NEROCD_MODE", "production")
	if err := migrateDatabase([]string{"--seed=true"}); err == nil {
		t.Fatal("production accepted development seed flag")
	}
}
