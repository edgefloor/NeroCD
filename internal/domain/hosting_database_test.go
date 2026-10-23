package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingDatabasePinnedMetadataRecipes(t *testing.T) {
	for _, test := range []struct {
		engine, version, mount, image string
		port                          int
		roles, users                  []string
	}{
		{"postgresql", HostingDatabasePostgresRecipe, "/var/lib/postgresql/data", "docker.io/library/postgres@sha256:858bb5e98eed02eb8cafbcdc007f9a65d1b613972028944a8ec2133ccaf80d27", 5432, []string{"admin", "app"}, []string{"postgres", "ncd_app"}},
		{"redis", HostingDatabaseRedisRecipe, "/data", "docker.io/library/redis@sha256:e5f23377739f95a2f530b9c99f93accb4f634577f09d00a7d8e0638f2fdf69e0", 6379, []string{"app", "probe"}, []string{"ncd_app", "ncd_probe"}},
	} {
		t.Run(test.engine, func(t *testing.T) {
			r, err := HostingDatabaseRecipeFor(test.engine, test.version)
			if err != nil || r.ImageReference != test.image || ValidateImageReference(r.ImageReference) != nil || r.MountPath != test.mount || r.ContainerPort != test.port || len(r.Credentials) != 2 || len(r.Platforms) != 2 {
				t.Fatalf("invalid pinned recipe %+v %v", r, err)
			}
			for i, ref := range r.Credentials {
				if ref.Role != test.roles[i] || ref.Username != test.users[i] || ref.SecretID != "" || ref.Version != 0 {
					t.Fatalf("recipe manufactured credential: %+v", ref)
				}
			}
			for _, p := range r.Platforms {
				if p.OS != "linux" || p.Architecture != "amd64" && p.Architecture != "arm64" || !imageSHA256.MatchString(p.Digest) {
					t.Fatalf("invalid observed native digest: %+v", p)
				}
			}
			r.Credentials[0].Username = "changed"
			r.Platforms[0].Digest = "changed"
			again, err := HostingDatabaseRecipeFor(test.engine, test.version)
			if err != nil || again.Credentials[0].Username != test.users[0] || !imageSHA256.MatchString(again.Platforms[0].Digest) {
				t.Fatal("recipe aliases caller-owned slices")
			}
		})
	}
	for _, pair := range [][2]string{{"postgresql", HostingDatabaseRedisRecipe}, {"redis", HostingDatabasePostgresRecipe}, {"mysql", "latest"}, {"postgresql", "latest"}} {
		if _, err := HostingDatabaseRecipeFor(pair[0], pair[1]); err == nil {
			t.Fatalf("unapproved recipe accepted: %v", pair)
		}
	}
}

func TestHostingDatabaseCreateInputClosedJSON(t *testing.T) {
	in := HostingDatabaseCreateInput{RequestID: strings.Repeat("a", 32), ProjectID: "proj_platform", ProjectEnvironmentID: "penv_database", Name: "Application database", DestinationID: "dest_database", Engine: "postgresql", RecipeVersion: HostingDatabasePostgresRecipe}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingDatabaseCreateInput
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded != in {
		t.Fatalf("valid database input %+v %v", decoded, err)
	}
	for _, bad := range []string{`null`, `[]`, `{}`, string(raw) + `{}`, strings.TrimSuffix(string(raw), "}") + `,"password":"caller-secret"}`, strings.TrimSuffix(string(raw), "}") + `,"engine":"redis"}`, strings.Replace(string(raw), `"name"`, `"Name"`, 1), strings.Replace(string(raw), `"Application database"`, `null`, 1), strings.Replace(string(raw), `"Application database"`, `5`, 1), strings.Replace(string(raw), `"Application database"`, `" trailing "`, 1), strings.Replace(string(raw), strings.Repeat("a", 32), strings.Repeat("A", 32), 1), strings.Replace(string(raw), `"proj_platform"`, `"bad/project"`, 1)} {
		if err = json.Unmarshal([]byte(bad), &decoded); err == nil {
			t.Errorf("ambiguous database input accepted: %s", bad)
		}
	}
	first := HostingDatabaseResourceID(in.ProjectID, in.RequestID)
	if first != HostingDatabaseResourceID(in.ProjectID, in.RequestID) || first == HostingDatabaseResourceID("proj_other", in.RequestID) || HostingDatabaseSecretID(first, "admin") == HostingDatabaseSecretID(first, "app") {
		t.Fatal("request or credential identities alias different scope")
	}
}
