package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"nerocd/internal/domain"
)

// loadHostingBuildPolicy reads only trusted operator configuration. Duration
// fields use Go JSON's integer nanoseconds; no target platform is inferred.
func loadHostingBuildPolicy() (*domain.HostingBuildPolicy, error) {
	path := os.Getenv("NEROCD_HOSTING_BUILD_POLICY_FILE")
	if path == "" {
		return nil, nil
	}
	if strings.TrimSpace(path) != path {
		return nil, errors.New("invalid hosting build policy file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("hosting build policy cannot be read")
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
		return nil, errors.New("hosting build policy must be a bounded regular file")
	}
	var policy domain.HostingBuildPolicy
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&policy); err != nil {
		return nil, errors.New("invalid hosting build policy document")
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid hosting build policy document")
	}
	if err = domain.ValidateHostingBuildPolicy(policy); err != nil {
		return nil, errors.New("invalid hosting build policy values")
	}
	return &policy, nil
}
