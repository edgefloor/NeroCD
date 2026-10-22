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
