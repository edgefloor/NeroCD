//go:build unix

package serverbootstrap

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openArtifact(source ArtifactSource, expected Artifact) (*os.File, string, error) {
	if !filepath.IsAbs(source.Path) || filepath.Clean(source.Path) != source.Path || source.Path == "/" || expected.Size <= 0 || expected.Size > maxArtifactSize {
		return nil, "", InvalidArtifact
	}
	directory, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", InvalidArtifact
	}
	parts := strings.Split(strings.TrimPrefix(source.Path, "/"), "/")
	for _, component := range parts[:len(parts)-1] {
		next, err := unix.Openat(directory, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(directory) // The next directory owns traversal after this point.
		if err != nil {
			return nil, "", InvalidArtifact
		}
		directory = next
	}
	fd, err := unix.Openat(directory, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(directory) // No longer needed after opening the one artifact FD.
	if err != nil {
		return nil, "", InvalidArtifact
	}
	file := os.NewFile(uintptr(fd), source.Path)
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected.Size {
		return nil, "", InvalidArtifact
	}
	binary, err := elf.NewFile(file)
	if err != nil || binary.Class != elf.ELFCLASS64 || binary.Data != elf.ELFDATA2LSB ||
		(binary.OSABI != elf.ELFOSABI_NONE && binary.OSABI != elf.ELFOSABI_LINUX) ||
		(binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) ||
		(expected.Architecture == "amd64" && binary.Machine != elf.EM_X86_64) ||
		(expected.Architecture == "arm64" && binary.Machine != elf.EM_AARCH64) ||
		(expected.Architecture != "amd64" && expected.Architecture != "arm64") {
		return nil, "", InvalidArtifact
	}
	hash := sha256.New()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", InvalidArtifact
	}
	count, err := io.Copy(hash, io.LimitReader(file, maxArtifactSize+1))
	if err != nil || count != expected.Size || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return nil, "", InvalidArtifact
	}
	header := make([]byte, 20)
	if _, err := file.ReadAt(header, 0); err != nil {
		return nil, "", InvalidArtifact
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", InvalidArtifact
	}
	accepted = true
	return file, hex.EncodeToString(header), nil
}
