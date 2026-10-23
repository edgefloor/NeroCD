package serverbootstrap

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Install stages and starts only an exact new owned installation. It never
// replaces another agent. Uncertain outcomes require InspectOwned before action.
func Install(ctx context.Context, target Target, key []byte, request InstallRequest) (OwnedInspection, error) {
	if request.OperationID != request.Manifest.InstallOperationID || !enrollmentPattern.Match(request.EnrollmentToken) {
		return OwnedInspection{}, InvalidManifest
	}
	if _, _, err := manifestBytes(request.Manifest); err != nil {
		return OwnedInspection{}, err
	}
	artifact, _, err := openArtifact(request.Artifact, request.Manifest.Artifact)
	if err != nil {
		return OwnedInspection{}, err
	}
	defer func() { _ = artifact.Close() }() // The same verified FD supplies transfer bytes.
	observations, err := Probe(ctx, target, key)
	if err != nil {
		return OwnedInspection{}, err
	}
	if observations.OS.State != Available || observations.OS.Architecture != request.Manifest.Artifact.Architecture || observations.Systemd.State != Available || observations.Docker.State != Available || observations.Docker.APIVersion != "1.54" {
		return OwnedInspection{}, RemoteFailure
	}
	return ownedAction(ctx, target, key, "install", request.OperationID, request.Manifest, artifact, request.EnrollmentToken)
}

// InspectOwned reads exact owned state without installing, mutating, or renewing anything.
func InspectOwned(ctx context.Context, target Target, key []byte, ownership Ownership) (OwnedInspection, error) {
	return ownedAction(ctx, target, key, "inspect", ownership.Manifest.InstallOperationID, ownership.Manifest, nil, nil)
}

// RestartOwned requests one exact owned service restart with immutable operation identity.
func RestartOwned(ctx context.Context, target Target, key []byte, mutation Mutation) (OwnedInspection, error) {
	return ownedAction(ctx, target, key, "restart", mutation.OperationID, mutation.Ownership.Manifest, nil, nil)
}

// CleanupOwned stops/removes only the exact owned agent and preserves a nonsecret tombstone.
func CleanupOwned(ctx context.Context, target Target, key []byte, mutation Mutation) (OwnedInspection, error) {
	return ownedAction(ctx, target, key, "cleanup", mutation.OperationID, mutation.Ownership.Manifest, nil, nil)
}

type ownedResponse struct {
	Code       ErrorCode        `json:"code,omitempty"`
	Inspection *OwnedInspection `json:"inspection,omitempty"`
}

func ownedAction(parent context.Context, target Target, key []byte, action, operation string, manifest InstallManifest, artifact io.Reader, enrollment []byte) (OwnedInspection, error) {
	metadata, digest, err := manifestBytes(manifest)
	if err != nil || !operationIDPattern.MatchString(operation) {
		return OwnedInspection{}, InvalidManifest
	}
	if !validTarget(target) {
		return OwnedInspection{}, InvalidTarget
	}
	if parent == nil {
		return OwnedInspection{}, DeadlineRequired
	}
	if _, ok := parent.Deadline(); !ok {
		return OwnedInspection{}, DeadlineRequired
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return OwnedInspection{}, operationError(ctx, TransportFailed)
	}
	if len(key) == 0 || len(key) > 65536 {
		return OwnedInspection{}, InvalidPrivateKey
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return OwnedInspection{}, InvalidPrivateKey
	}
	if public, ok := signer.PublicKey().(ssh.CryptoPublicKey); ok {
		if rsaKey, ok := public.CryptoPublicKey().(*rsa.PublicKey); ok && rsaKey.N.BitLen() > 8192 {
			return OwnedInspection{}, InvalidPrivateKey
		}
	}
	address := net.JoinHostPort(target.Address.Unmap().String(), strconv.Itoa(int(target.Port)))
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return OwnedInspection{}, operationError(ctx, TransportFailed)
	}
	deadline, _ := ctx.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		_ = raw.Close()
		return OwnedInspection{}, TransportFailed
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-stop:
		}
	}()
	defer func() { close(stop); _ = raw.Close(); <-stopped }()
	var mismatch atomic.Bool
	configuration := &ssh.ClientConfig{User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: func(_ string, _ net.Addr, public ssh.PublicKey) error {
		if ssh.FingerprintSHA256(public) != target.HostKeyFingerprint {
			mismatch.Store(true)
			return HostKeyMismatch
		}
		return nil
	}}
	connection, channels, requests, err := ssh.NewClientConn(raw, address, configuration)
	if err != nil {
		if mismatch.Load() {
			return OwnedInspection{}, HostKeyMismatch
		}
		return OwnedInspection{}, operationError(ctx, HandshakeFailed)
	}
	client := ssh.NewClient(connection, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return OwnedInspection{}, operationError(ctx, TransportFailed)
	}
	defer func() { _ = session.Close() }()
	header := fmt.Sprintf("nerocd_owned_v1\n%s\n%s\n%s\n%s\n%d\n%s\n%s\n%d\n%s\n%d\n", action, manifest.InstallationID, operation, manifest.InstallOperationID, manifest.Artifact.Size, manifest.Artifact.SHA256, manifest.Artifact.Architecture, len(metadata), digest, len(enrollment))
	if len(header) > 2048 {
		return OwnedInspection{}, InvalidManifest
	}
	if artifact == nil {
		artifact = strings.NewReader("")
	}
	session.Stdin = io.MultiReader(strings.NewReader(header), bytes.NewReader(metadata), io.LimitReader(artifact, manifest.Artifact.Size), bytes.NewReader(enrollment))
	output := &boundedOutput{closeTransport: raw.Close}
	session.Stdout, session.Stderr = output, output
	command := "exec /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C /usr/bin/timeout --signal=TERM --kill-after=5s 120s /bin/sh -c '" + strings.ReplaceAll(ownedRecipe, "'", "'\"'\"'") + "'"
	runErr := session.Run(command)
	if output.exceeded {
		return OwnedInspection{}, Uncertain
	}
	var response ownedResponse
	decoder := json.NewDecoder(bytes.NewReader(output.data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return OwnedInspection{}, Uncertain
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return OwnedInspection{}, Uncertain
	}
	if response.Code != "" {
		switch response.Code {
		case InvalidManifest, InvalidArtifact, OwnershipConflict, RemoteBusy, PartialTransfer, RemoteFailure, Uncertain:
			return OwnedInspection{}, response.Code
		default:
			return OwnedInspection{}, Uncertain
		}
	}
	if runErr != nil || response.Inspection == nil {
		return OwnedInspection{}, Uncertain
	}
	result := *response.Inspection
	if result.InstallationID != manifest.InstallationID || result.ManifestSHA256 != digest || !operationIDPattern.MatchString(result.OperationID) ||
		(result.InvocationID != "" && !operationIDPattern.MatchString(result.InvocationID)) {
		return OwnedInspection{}, Uncertain
	}
	switch result.State {
	case OwnedAbsent, OwnedStaged, OwnedInstalled, OwnedRemoved, OwnedUncertain:
		return result, nil
	default:
		return OwnedInspection{}, Uncertain
	}
}
