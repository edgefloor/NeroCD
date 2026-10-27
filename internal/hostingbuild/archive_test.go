package hostingbuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureArchive(t *testing.T, mutate func(map[string][]byte)) string {
	t.Helper()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	content := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "hello", Mode: 0644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	if _, err := gz.Write(layer.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"os": "linux", "architecture": "arm64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{sum(layer.Bytes())}}})
	configDigest := sum(config)
	layerDigest := sum(zipped.Bytes())
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaManifest, "config": descriptor{MediaType: mediaConfig, Digest: configDigest, Size: int64(len(config))}, "layers": []descriptor{{MediaType: mediaLayer, Digest: layerDigest, Size: int64(zipped.Len())}}})
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaIndex, "manifests": []descriptor{{MediaType: mediaManifest, Digest: sum(manifest), Size: int64(len(manifest))}}})
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`), "index.json": index, "blobs/sha256/" + strings.TrimPrefix(configDigest, "sha256:"): config, "blobs/sha256/" + strings.TrimPrefix(layerDigest, "sha256:"): zipped.Bytes(), "blobs/sha256/" + strings.TrimPrefix(sum(manifest), "sha256:"): manifest}
	if mutate != nil {
		mutate(files)
	}
	archive := filepath.Join(t.TempDir(), "image.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw = tar.NewWriter(f)
	for name, b := range files {
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return archive
}
func TestOCIValidation(t *testing.T) {
	for _, kind := range []string{"valid", "traversal", "digest", "platform", "export-limit", "unpacked-limit", "unreferenced"} {
		t.Run(kind, func(t *testing.T) {
			archive := fixtureArchive(t, func(files map[string][]byte) {
				switch kind {
				case "traversal":
					files["../escape"] = []byte("bad")
				case "digest":
					for name := range files {
						if strings.HasPrefix(name, "blobs/") {
							files[name] = []byte("tampered")
							break
						}
					}
				case "unreferenced":
					files["blobs/sha256/"+strings.TrimPrefix(sum([]byte("unreferenced")), "sha256:")] = []byte("unreferenced")
				}
			})
			l := testLimits()
			platform := "linux/arm64"
			switch kind {
			case "platform":
				platform = "linux/amd64"
			case "export-limit":
				l.ExportBytes = 100
			case "unpacked-limit":
				l.UnpackedBytes = 1
			}
			identity, err := validateExport(context.Background(), archive, filepath.Join(t.TempDir(), "validated"), platform, l)
			if kind == "valid" {
				if err != nil || !digestPattern.MatchString(identity.Config) || !digestPattern.MatchString(identity.Manifest) {
					t.Fatalf("valid OCI rejected: %#v %v", identity, err)
				}
			} else if err == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}

func TestOCIOuterLinksRejected(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "link.tar")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err = tw.WriteHeader(&tar.Header{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = validateExport(t.Context(), archive, filepath.Join(t.TempDir(), "validated"), "linux/arm64", testLimits()); err == nil {
		t.Fatal("outer OCI symlink accepted")
	}
}

func TestOCICompatibleDockerManifest(t *testing.T) {
	for _, mode := range []string{"legacy", "valid", "config", "layers", "duplicate-entry", "traversal", "tag", "unknown", "duplicate-key", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			archive := fixtureArchive(t, func(files map[string][]byte) {
				if mode == "legacy" {
					return
				}
				var index struct {
					Manifests []descriptor `json:"manifests"`
				}
				if err := json.Unmarshal(files["index.json"], &index); err != nil {
					t.Fatal(err)
				}
				var manifest struct {
					Config descriptor   `json:"config"`
					Layers []descriptor `json:"layers"`
				}
				if err := json.Unmarshal(files["blobs/sha256/"+strings.TrimPrefix(index.Manifests[0].Digest, "sha256:")], &manifest); err != nil {
					t.Fatal(err)
				}
				config := "blobs/sha256/" + strings.TrimPrefix(manifest.Config.Digest, "sha256:")
				layers := []string{"blobs/sha256/" + strings.TrimPrefix(manifest.Layers[0].Digest, "sha256:")}
				entry := map[string]any{"Config": config, "Layers": layers, "RepoTags": []string{}}
				switch mode {
				case "config":
					entry["Config"] = layers[0]
				case "layers":
					entry["Layers"] = []string{config}
				case "traversal":
					entry["Layers"] = []string{"../" + layers[0]}
				case "tag":
					entry["RepoTags"] = []string{"existing:latest"}
				case "unknown":
					entry["Other"] = config
				}
				entries := []any{entry}
				if mode == "duplicate-entry" {
					entries = append(entries, entry)
				}
				raw, err := json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "duplicate-key" {
					raw = []byte(`[{"Config":"wrong","Config":"` + config + `","Layers":["` + layers[0] + `"]}]`)
				}
				if mode == "trailing" {
					raw = append(raw, []byte(` []`)...)
				}
				files["manifest.json"] = raw
			})
			identity, err := validateExport(t.Context(), archive, filepath.Join(t.TempDir(), "validated"), "linux/arm64", testLimits())
			if mode == "legacy" || mode == "valid" {
				if err != nil || !digestPattern.MatchString(identity.Config) {
					t.Fatalf("compatible export rejected: %v", err)
				}
			} else if err == nil {
				t.Fatalf("unsafe Docker compatibility mapping accepted: %s", mode)
			}
		})
	}
}

func TestDockerImportManifestPreservesLayerOrder(t *testing.T) {
	config := "blobs/sha256/" + strings.TrimPrefix(sum([]byte("config")), "sha256:")
	layers := []descriptor{{Digest: sum([]byte("first"))}, {Digest: sum([]byte("second"))}}
	paths := []string{"blobs/sha256/" + strings.TrimPrefix(layers[0].Digest, "sha256:"), "blobs/sha256/" + strings.TrimPrefix(layers[1].Digest, "sha256:")}
	for _, reversed := range []bool{false, true} {
		if reversed {
			paths[0], paths[1] = paths[1], paths[0]
		}
		raw, err := json.Marshal([]any{map[string]any{"Config": config, "Layers": paths}})
		if err != nil {
			t.Fatal(err)
		}
		if err := validateDockerImportManifest(raw, config, layers); (err != nil) != reversed {
			t.Fatalf("Docker import layer ordering reversed=%t: %v", reversed, err)
		}
	}
}
