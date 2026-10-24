package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testAlias = "ncdb-0123456789abcdef0123456789abcdef"

func TestPrivateEndpointIPCompatibility(t *testing.T) {
	for _, host := range []string{"10.0.0.2", "172.18.0.3", "192.168.2.4", "::ffff:172.18.0.3"} {
		t.Run(host, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), host, "", func(context.Context, string) ([]string, error) {
				t.Fatal("IP-only mode must not resolve DNS")
				return nil, nil
			})
			if err != nil || got != host {
				t.Fatalf("IP-only endpoint changed: got %q, error %v", got, err)
			}
		})
	}
}

func TestPrivateEndpointAliasPinsInspectedIPv4(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	calls := 0
	got, err := privateEndpoint(ctx, testAlias, "172.18.0.3", func(received context.Context, host string) ([]string, error) {
		calls++
		if received != ctx || host != testAlias {
			t.Fatal("lookup must receive the exact alias and shared deadline context")
		}
		return []string{"172.18.0.3"}, nil
	})
	if err != nil || got != "172.18.0.3" || calls != 1 {
		t.Fatalf("alias did not produce the inspected IPv4: got %q, calls %d, error %v", got, calls, err)
	}
}

func TestPrivateEndpointRejectsUnboundedOrAmbiguousInputBeforeDNS(t *testing.T) {
	tests := []struct{ host, address string }{
		{"127.0.0.1", ""}, {"8.8.8.8", ""}, {"::1", ""}, {"fd00::1", ""}, {testAlias, ""},
		{"example.invalid", "172.18.0.3"}, {"172.18.0.3", "172.18.0.3"},
		{strings.ToUpper(testAlias), "172.18.0.3"}, {testAlias + ".", "172.18.0.3"},
		{testAlias + ".internal", "172.18.0.3"}, {testAlias[:36], "172.18.0.3"},
		{testAlias + "0", "172.18.0.3"}, {testAlias[:36] + "g", "172.18.0.3"},
		{testAlias, "8.8.8.8"}, {testAlias, "127.0.0.1"}, {testAlias, "169.254.1.2"},
		{testAlias, "fd00::1"}, {testAlias, "::ffff:172.18.0.3"},
		{testAlias, "172.018.0.3"}, {testAlias, "172.18.0.3:5432"},
	}
	for _, test := range tests {
		t.Run(test.host+"/"+test.address, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), test.host, test.address, func(context.Context, string) ([]string, error) {
				t.Fatal("invalid endpoint reached DNS")
				return nil, nil
			})
			if err == nil || got != "" {
				t.Fatal("invalid endpoint accepted")
			}
		})
	}
}

func TestPrivateEndpointRejectsDNSOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		addresses []string
		err       error
	}{
		{"empty", nil, nil},
		{"mismatched", []string{"172.18.0.4"}, nil},
		{"nonprivate", []string{"8.8.8.8"}, nil},
		{"ipv6", []string{"fd00::1"}, nil},
		{"mapped_ipv6", []string{"::ffff:172.18.0.3"}, nil},
		{"multiple", []string{"172.18.0.3", "172.18.0.4"}, nil},
		{"duplicate", []string{"172.18.0.3", "172.18.0.3"}, nil},
		{"mixed", []string{"172.18.0.3", "8.8.8.8"}, nil},
		{"partial_error", []string{"172.18.0.3"}, errors.New("sensitive resolver diagnostic")},
		{"error", nil, errors.New("sensitive resolver diagnostic")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := privateEndpoint(t.Context(), testAlias, "172.18.0.3", func(context.Context, string) ([]string, error) {
				return test.addresses, test.err
			})
			if got != "" || err == nil || err.Error() != "private alias qualification failed" {
				t.Fatalf("DNS result did not fail closed with generic error: got %q, error %v", got, err)
			}
		})
	}
}

func TestPrivateEndpointLookupDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	got, err := privateEndpoint(ctx, testAlias, "172.18.0.3", func(received context.Context, _ string) ([]string, error) {
		<-received.Done()
		return nil, received.Err()
	})
	if got != "" || err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("lookup did not honor the shared deadline")
	}
}

func TestClientFailureOutputHelper(t *testing.T) {
	if os.Getenv("NEROCD_MARKER_CLIENT_OUTPUT_TEST") == "1" {
		main()
	}
}

func TestClientFailureOutputAndInputBounds(t *testing.T) {
	password := []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	credential := input{Engine: "postgresql", Host: "example.invalid", Address: "172.18.0.3", Username: "ncd_app", Password: password, Marker: "private_marker_0123456789", Write: true}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{raw, append(bytes.Clone(raw), []byte(strings.Repeat(" ", 4097))...), []byte(`{"password":"parser_diagnostic_marker"`)} {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClientFailureOutputHelper$")
		cmd.Env = append(os.Environ(), "NEROCD_MARKER_CLIENT_OUTPUT_TEST=1")
		cmd.Stdin = bytes.NewReader(raw)
		out, err := cmd.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || string(out) != "database marker client failed\n" {
			t.Fatalf("failure output or exit contract changed: %q, error %v", out, err)
		}
	}
}
