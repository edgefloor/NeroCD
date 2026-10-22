package hostingbuild

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	mediaManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaIndex    = "application/vnd.oci.image.index.v1+json"
	mediaConfig   = "application/vnd.oci.image.config.v1+json"
	mediaLayer    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}
type imageIdentity struct{ Index, Manifest, Config, Platform string }

// validateExport never extracts image layers. The outer OCI layout is extracted
// into a private directory with a fixed filename grammar and no links.
func validateExport(ctx context.Context, archive, dir, platform string, l Limits) (imageIdentity, error) {
	var out imageIdentity
	f, err := os.Open(archive)
	if err != nil {
		return out, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !st.Mode().IsRegular() || st.Size() > l.ExportBytes {
		return out, errors.New("export byte budget exceeded")
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return out, err
	}
	tr := tar.NewReader(io.LimitReader(contextReader{ctx: ctx, reader: f}, l.ExportBytes+1))
	seen := map[string]int64{}
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
		name := h.Name
		if h.Typeflag == tar.TypeDir && (name == "blobs/" || name == "blobs/sha256/") {
			continue
		}
		valid := name == "index.json" || name == "oci-layout" || (strings.HasPrefix(name, "blobs/sha256/") && digestPattern.MatchString("sha256:"+strings.TrimPrefix(name, "blobs/sha256/")))
		if !valid || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > l.ExportBytes-total {
			return out, errors.New("invalid OCI archive member or size")
		}
		if _, ok := seen[name]; ok {
			return out, errors.New("duplicate OCI archive member")
		}
		if len(seen) >= 100000 {
			return out, errors.New("OCI archive member count exceeded")
		}
		seen[name] = h.Size
		total += h.Size
		if err = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			return out, err
		}
		dst, e := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return out, e
		}
		hasher := sha256.New()
		n, e := io.Copy(io.MultiWriter(dst, hasher), tr)
		closeErr := dst.Close()
		if e != nil {
			return out, e
		}
		if closeErr != nil {
			return out, closeErr
		}
		if n != h.Size {
			return out, io.ErrUnexpectedEOF
		}
		if strings.HasPrefix(name, "blobs/") && hex.EncodeToString(hasher.Sum(nil)) != path.Base(name) {
			return out, errors.New("OCI blob digest mismatch")
		}
	}
	read := func(name string) ([]byte, error) {
		if seen[name] > 4<<20 {
			return nil, errors.New("OCI metadata exceeds 4MiB")
		}
		return os.ReadFile(filepath.Join(dir, name))
	}
	layout, err := read("oci-layout")
	if err != nil {
		return out, err
	}
	var layoutValue struct {
		Version string `json:"imageLayoutVersion"`
	}
	if json.Unmarshal(layout, &layoutValue) != nil || layoutValue.Version != "1.0.0" {
		return out, errors.New("invalid OCI layout")
	}
	indexBytes, err := read("index.json")
	if err != nil {
		return out, err
	}
	var index struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []descriptor `json:"manifests"`
	}
	if json.Unmarshal(indexBytes, &index) != nil || index.SchemaVersion != 2 || (index.MediaType != "" && index.MediaType != mediaIndex) || len(index.Manifests) != 1 {
		return out, errors.New("single OCI manifest required")
	}
	resolve := func(d descriptor, media string) (string, error) {
		if d.MediaType != media || !digestPattern.MatchString(d.Digest) || d.Size < 0 {
			return "", errors.New("unsupported OCI descriptor")
		}
		name := "blobs/sha256/" + strings.TrimPrefix(d.Digest, "sha256:")
		size, ok := seen[name]
		if !ok || size != d.Size {
			return "", errors.New("OCI descriptor size mismatch")
		}
		return name, nil
	}
	manifest := index.Manifests[0]
	mp, err := resolve(manifest, mediaManifest)
	if err != nil {
		return out, err
	}
	if manifest.Platform.OS != "" && manifest.Platform.OS+"/"+manifest.Platform.Architecture != platform {
		return out, errors.New("OCI manifest platform mismatch")
	}
	mb, err := read(mp)
	if err != nil {
		return out, err
	}
	var m struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Config        descriptor   `json:"config"`
		Layers        []descriptor `json:"layers"`
	}
	if json.Unmarshal(mb, &m) != nil || m.SchemaVersion != 2 || m.MediaType != mediaManifest || len(m.Layers) > 10000 {
		return out, errors.New("invalid OCI image manifest")
	}
	cp, err := resolve(m.Config, mediaConfig)
	if err != nil {
		return out, err
	}
	cb, err := read(cp)
	if err != nil {
		return out, err
	}
	var c struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if json.Unmarshal(cb, &c) != nil || c.OS+"/"+c.Architecture != platform || c.RootFS.Type != "layers" || len(c.RootFS.DiffIDs) != len(m.Layers) {
		return out, errors.New("OCI image config/platform mismatch")
	}
	used := map[string]bool{cp: true, mp: true, "oci-layout": true, "index.json": true}
	var expanded int64
	for i, d := range m.Layers {
		lp, e := resolve(d, mediaLayer)
		if e != nil {
			return out, e
		}
		used[lp] = true
		layer, e := os.Open(filepath.Join(dir, lp))
		if e != nil {
			return out, e
		}
		gz, e := gzip.NewReader(layer)
		if e != nil {
			_ = layer.Close()
			return out, e
		}
		hash := sha256.New()
		limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: gz}, N: l.UnpackedBytes - expanded + 1}
		tee := io.TeeReader(limited, hash)
		layerTar := tar.NewReader(tee)
		entries := 0
		for {
			if e = ctx.Err(); e != nil {
				break
			}
			header, nextErr := layerTar.Next()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				e = nextErr
				break
			}
			entries++
			clean := path.Clean(header.Name)
			if entries > 1000000 || strings.HasPrefix(header.Name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || header.Size < 0 || header.Size > limited.N {
				e = errors.New("invalid or oversized image layer member")
				break
			}
			// Image links are metadata, never host extraction. Their target is
			// interpreted relative to the image root, as required by OCI.
			if header.Typeflag == tar.TypeLink && (strings.HasPrefix(header.Linkname, "/") || path.Clean(header.Linkname) == ".." || strings.HasPrefix(path.Clean(header.Linkname), "../")) {
				e = errors.New("invalid image hardlink target")
				break
			}
		}
		if e == nil {
			_, e = io.Copy(io.Discard, tee)
		}
		n := l.UnpackedBytes - expanded + 1 - limited.N
		_ = gz.Close()
		_ = layer.Close()
		expanded += n
		if e != nil {
			return out, e
		}
		if expanded > l.UnpackedBytes {
			return out, errors.New("unpacked image byte budget exceeded")
		}
		if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != c.RootFS.DiffIDs[i] {
			return out, errors.New("OCI layer diffID mismatch")
		}
	}
	for name := range seen {
		if !used[name] {
			return out, fmt.Errorf("unreferenced OCI member: %s", name)
		}
	}
	return imageIdentity{Index: sum(indexBytes), Manifest: manifest.Digest, Config: m.Config.Digest, Platform: platform}, nil
}
