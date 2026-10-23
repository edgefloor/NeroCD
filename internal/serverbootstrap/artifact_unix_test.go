//go:build unix

package serverbootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func artifactFixture(t *testing.T) (string, Artifact, []byte) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 183)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	path := filepath.Join(directory, "agent")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return path, Artifact{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Architecture: "arm64", Version: "1.0.0-fixture"}, data
}

func TestArtifactUsesVerifiedDescriptorAfterPathSubstitution(t *testing.T) {
	path, expected, original := artifactFixture(t)
	file, _, err := openArtifact(ArtifactSource{Path: path}, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("substituted"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("transfer did not retain verified descriptor")
	}
}

func TestArtifactRejectsUnsafeSourceAndInventory(t *testing.T) {
	for _, name := range []string{"symlink", "ancestor symlink", "directory", "wrong digest", "wrong size", "oversized", "wrong architecture", "relative", "non-elf"} {
		t.Run(name, func(t *testing.T) {
			path, expected, _ := artifactFixture(t)
			switch name {
			case "symlink":
				if err := os.Symlink(path, path+".link"); err != nil {
					t.Fatal(err)
				}
				path += ".link"
			case "ancestor symlink":
				link := filepath.Join(filepath.Dir(path), "link")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, "agent")
			case "directory":
				path = filepath.Dir(path)
			case "wrong digest":
				expected.SHA256 = string(bytes.Repeat([]byte("0"), 64))
			case "wrong size":
				expected.Size++
			case "oversized":
				expected.Size = maxArtifactSize + 1
			case "wrong architecture":
				expected.Architecture = "amd64"
			case "relative":
				path = "agent"
			case "non-elf":
				if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 64), 0600); err != nil {
					t.Fatal(err)
				}
			}
			file, _, err := openArtifact(ArtifactSource{Path: path}, expected)
			if file != nil {
				_ = file.Close()
				t.Fatal("unsafe artifact admitted")
			}
			if err != InvalidArtifact {
				t.Fatalf("got %v", err)
			}
		})
	}
}
