package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"nerocd/internal/app"
)

// loadServerBootstrapConfig accepts only an explicitly provisioned operator file.
// The policy contains no SSH or enrollment secrets and is disabled by default.
func loadServerBootstrapConfig() (*app.ServerBootstrapConfig, error) {
	path := strings.TrimSpace(os.Getenv("NEROCD_SERVER_BOOTSTRAP_CONFIG_FILE"))
	if path == "" {
		return nil, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("server bootstrap configuration unavailable")
	}
	defer func() { _ = f.Close() }()
	raw, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(raw) > 65536 {
		return nil, errors.New("invalid server bootstrap configuration")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg app.ServerBootstrapConfig
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid server bootstrap configuration")
	}
	return &cfg, nil
}
