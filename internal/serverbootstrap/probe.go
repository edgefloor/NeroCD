// Package serverbootstrap observes prerequisites over an explicitly trusted SSH
// target. It does not install software, enroll a runner, or authorize deployment.
package serverbootstrap

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target identifies an operator-authorized literal IP and independently trusted
// host fingerprint. Private and loopback addresses do not imply sandboxing.
type Target struct {
	Address            netip.Addr
	Port               uint16
	User               string
	HostKeyFingerprint string
}

// ErrorCode is a fixed safe failure code. It never contains remote output,
// transport causes, target details, or private key material.
type ErrorCode string

// Probe failure codes distinguish admission, trust, transport, and output errors.
const (
	InvalidTarget       ErrorCode = "invalid_target"
	DeadlineRequired    ErrorCode = "deadline_required"
	InvalidPrivateKey   ErrorCode = "invalid_private_key"
	HostKeyMismatch     ErrorCode = "host_key_mismatch"
	HandshakeFailed     ErrorCode = "handshake_failed"
	TransportFailed     ErrorCode = "transport_failed"
	Canceled            ErrorCode = "canceled"
	DeadlineExceeded    ErrorCode = "deadline_exceeded"
	OutputLimitExceeded ErrorCode = "output_limit_exceeded"
	InvalidOutput       ErrorCode = "invalid_output"
)

func (code ErrorCode) Error() string { return "server bootstrap: " + string(code) }

// Is preserves standard context matching without exposing an underlying error.
func (code ErrorCode) Is(target error) bool {
	return (code == Canceled && target == context.Canceled) ||
		(code == DeadlineExceeded && target == context.DeadlineExceeded)
}

const maxOutput = 16 * 1024

// Probe runs one fixed read-only recipe. A caller deadline is mandatory and the
// network lifetime is additionally capped at 15 seconds. Synchronous key parsing
// and cryptographic work are not context-preemptible. Failures return zero
// observations. The caller owns key bytes; this function neither retains nor
// logs them and does not promise that Go erases key material from memory.
func Probe(ctx context.Context, target Target, privateKey []byte) (Observations, error) {
	if !validTarget(target) {
		return Observations{}, InvalidTarget
	}
	if ctx == nil {
		return Observations{}, DeadlineRequired
	}
	if _, ok := ctx.Deadline(); !ok {
		return Observations{}, DeadlineRequired
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Observations{}, operationError(ctx, TransportFailed)
	}
	if len(privateKey) == 0 || len(privateKey) > 64*1024 {
		return Observations{}, InvalidPrivateKey
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return Observations{}, InvalidPrivateKey
	}
	if public, ok := signer.PublicKey().(ssh.CryptoPublicKey); ok {
		if key, ok := public.CryptoPublicKey().(*rsa.PublicKey); ok && key.N.BitLen() > 8192 {
			return Observations{}, InvalidPrivateKey
		}
	}
	address := net.JoinHostPort(target.Address.Unmap().String(), strconv.Itoa(int(target.Port)))
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return Observations{}, operationError(ctx, TransportFailed)
	}
	deadline, _ := ctx.Deadline() // The mandatory deadline is preserved by WithTimeout.
	if err := raw.SetDeadline(deadline); err != nil {
		_ = raw.Close() // Best-effort transport teardown; no remote cause is exposed.
		return Observations{}, TransportFailed
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = raw.Close() // Cancellation owns teardown, including incomplete handshakes.
		case <-stop:
		}
	}()
	defer func() {
		close(stop)
		_ = raw.Close() // Closing the transport releases SSH's internal readers.
		<-stopped
	}()
	var mismatch atomic.Bool
	configuration := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != target.HostKeyFingerprint {
				mismatch.Store(true)
				return HostKeyMismatch
			}
			return nil
		},
	}
	connection, channels, requests, err := ssh.NewClientConn(raw, address, configuration)
	if err != nil {
		if mismatch.Load() {
			return Observations{}, HostKeyMismatch
		}
		return Observations{}, operationError(ctx, HandshakeFailed)
	}
	client := ssh.NewClient(connection, channels, requests)
	defer func() { _ = client.Close() }() // Teardown errors do not replace probe evidence.
	session, err := client.NewSession()
	if err != nil {
		return Observations{}, operationError(ctx, TransportFailed)
	}
	defer func() { _ = session.Close() }() // Run has completed or transport cancellation released it.
	output := &boundedOutput{closeTransport: raw.Close}
	session.Stdout = output
	session.Stderr = output
	session.Stdin = strings.NewReader(probeRecipe)
	err = session.Run("env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C /bin/sh -s")
	if output.exceeded {
		return Observations{}, OutputLimitExceeded
	}
	if err != nil {
		return Observations{}, operationError(ctx, TransportFailed)
	}
	if ctx.Err() != nil {
		return Observations{}, operationError(ctx, TransportFailed)
	}
	return parseObservations(output.data)
}

func validTarget(target Target) bool {
	address := target.Address
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	unicast := address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast()
	if address.IsUnspecified() || address.IsMulticast() ||
		!unicast || target.Port == 0 || target.User != "root" {
		return false
	}
	encoded, ok := strings.CutPrefix(target.HostKeyFingerprint, "SHA256:")
	if !ok {
		return false
	}
	digest, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	return err == nil && len(digest) == 32 && base64.RawStdEncoding.EncodeToString(digest) == encoded
}

func operationError(ctx context.Context, fallback ErrorCode) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return Canceled
	}
	deadline, _ := ctx.Deadline()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(deadline) {
		return DeadlineExceeded
	}
	return fallback
}

type boundedOutput struct {
	mu             sync.Mutex
	data           []byte
	exceeded       bool
	closeTransport func() error
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.exceeded || len(data) > maxOutput-len(output.data) {
		output.exceeded = true
		_ = output.closeTransport() // Immediate teardown bounds both SSH streams together.
		return 0, OutputLimitExceeded
	}
	output.data = append(output.data, data...)
	return len(data), nil
}
