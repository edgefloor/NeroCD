package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func wordpressInitialCatalogRequest(t *testing.T, environment, destination string) domain.HostingCatalogInstallInput {
	t.Helper()
	return domain.HostingCatalogInstallInput{RequestID: storageRemoteRuntimeRequestID(t), ProjectID: "proj_platform", ProjectEnvironmentID: environment, DestinationID: destination, Name: "WordPress initial deployment", TemplateID: "wordpress-with-mysql", ImmutableVersion: "coolify-b12d1386.wordpress-7.1.3-mysql-8.4.13.v1", PublicURL: "https://catalog-runtime.example.test:25443"}
}

func wordpressInitialDeploymentRequest(generation int64) domain.HostingDeploymentInput {
	return domain.HostingDeploymentInput{ExpectedGeneration: generation, ExpectedRuntimeConfigurationGeneration: 1, ManagedStack: &domain.HostingStackDeploymentInput{AcknowledgeInterruption: true}, IdempotencyKey: "wordpress-initial-runtime", ContainerPort: 80, HostPort: 20000, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 120}}
}

func TestWordPressCatalogFixtureRequestContract(t *testing.T) {
	input := wordpressInitialCatalogRequest(t, "env_fixture", "dest_fixture")
	raw, _ := json.Marshal(input)
	var decoded domain.HostingCatalogInstallInput
	if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/hosting-catalog/installations", bytes.NewReader(raw)), &decoded) || !reflect.DeepEqual(input, decoded) || decoded.Validate() != nil {
		t.Fatal("WordPress fixture does not match public catalog decoder")
	}
}

func TestWordPressFixtureExactTemplateAndDeploymentContract(t *testing.T) {
	template, ok := domain.LookupHostingCatalogTemplate("wordpress-with-mysql", domain.HostingCatalogWordPressVersion)
	if !ok || template.Definition.Compose == nil {
		t.Fatal("WordPress template unavailable")
	}
	const image = "wordpress:7.1.3-php8.3-apache@sha256:f6056dcd9e4c146cc9f150b99b1ec62d875712a66dcab4005042778b0310e834"
	if strings.Count(template.Definition.Compose.Original, image) != 1 {
		t.Fatal("WordPress fault must replace exactly one parsed public image")
	}
	changed := *template.Definition.Compose
	changed.Original = strings.Replace(changed.Original, image, "127.0.0.1:1/wordpress@sha256:"+strings.Repeat("a", 64), 1)
	definition, err := domain.ParseHostingStack(changed)
	if err != nil || definition.PublicComponent != "wordpress" || definition.ContainerPort != 80 {
		t.Fatal("preparation-fault source invalid")
	}
	input := wordpressInitialDeploymentRequest(1)
	for _, mode := range []string{"deploy", "replace", "start"} {
		if mode != "deploy" {
			input.StorageRouteContinuity = &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_fixture", Routes: []domain.HostingRouteSwitchSelection{{RouteID: "route_fixture", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_previous"}}}
		}
		if mode == "start" {
			input.ManagedStack.ExpectedStoppedOperationID = "hop_stopped"
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded domain.HostingDeploymentInput
		if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_fixture/deployments", bytes.NewReader(raw)), &decoded) || domain.ValidateHostingDeployment(decoded) != nil || !reflect.DeepEqual(decoded, input) {
			t.Fatalf("WordPress%s decoder changed", mode)
		}
	}
}

func TestWordPressComponentDatabaseFixtureRequests(t *testing.T) {
	backup := domain.HostingDatabaseBackupInput{RequestID: strings.Repeat("a", 32), Component: "mysql", ExpectedResourceGeneration: 3, ExpectedDeployedOperationID: "hop_source"}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatal(err)
	}
	var got domain.HostingDatabaseBackupInput
	if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/resources/res_source/database-backups", bytes.NewReader(raw)), &got) || !reflect.DeepEqual(backup, got) || got.Validate() != nil {
		t.Fatal("MySQL component backup fixture decoder contract changed")
	}
	restore := domain.HostingDatabaseBackupRestoreInput{RequestID: strings.Repeat("b", 32), ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: "res_target", Component: "mysql", ExpectedDeployedOperationID: "hop_target", ExpectedResourceGeneration: 2, AcknowledgeReplacement: true}}
	for _, tc := range []struct {
		name   string
		change func(*domain.HostingDatabaseBackupRestoreInput)
		valid  bool
	}{
		{"exact distinct reviewed target", func(*domain.HostingDatabaseBackupRestoreInput) {}, true},
		{"missing destructive acknowledgement", func(in *domain.HostingDatabaseBackupRestoreInput) { in.ComponentTarget.AcknowledgeReplacement = false }, false},
		{"missing generation", func(in *domain.HostingDatabaseBackupRestoreInput) { in.ComponentTarget.ExpectedResourceGeneration = 0 }, false},
		{"missing deployed identity", func(in *domain.HostingDatabaseBackupRestoreInput) {
			in.ComponentTarget.ExpectedDeployedOperationID = ""
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := restore
			target := *restore.ComponentTarget
			in.ComponentTarget = &target
			tc.change(&in)
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			var got domain.HostingDatabaseBackupRestoreInput
			if !decodeBody(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/database-backups/dbb_fixture/restores", bytes.NewReader(raw)), &got) || !reflect.DeepEqual(in, got) || (got.Validate() == nil) != tc.valid {
				t.Fatal("MySQL import fixture public decoder/authority fields changed")
			}
		})
	}
}

// Keep transport diagnostics closed: errors can include private URLs or values.
func wordpressRuntimeHTTPErrorCategory(err error) string {
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var invalidCertificate x509.CertificateInvalidError
	var hostname x509.HostnameError
	if errors.As(err, &invalidCertificate) && invalidCertificate.Reason == x509.Expired {
		if invalidCertificate.Cert != nil && time.Now().Before(invalidCertificate.Cert.NotBefore) {
			return "tls_not_yet_valid"
		}
		return "tls_expired"
	}
	if errors.As(err, &unknownAuthority) {
		return "tls_unknown_authority"
	}
	if errors.As(err, &hostname) {
		return "tls_hostname"
	}
	if errors.As(err, &certificate) || errors.As(err, &invalidCertificate) {
		return "tls_verification"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var request *url.Error
	if errors.As(err, &request) && request.Err != nil && request.Err.Error() == "stopped after 10 redirects" {
		return "redirect_limit"
	}
	var operation *net.OpError
	if errors.As(err, &operation) && operation.Op == "dial" {
		return "connect"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection_closed"
	}
	return "transport_other"
}

func TestWordPressRuntimeHTTPErrorCategories(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "tls_unknown_authority"},
		{"expired", x509.CertificateInvalidError{Cert: &x509.Certificate{NotAfter: time.Now().Add(-time.Hour)}, Reason: x509.Expired}, "tls_expired"},
		{"not_yet_valid", x509.CertificateInvalidError{Cert: &x509.Certificate{NotBefore: time.Now().Add(time.Hour)}, Reason: x509.Expired}, "tls_not_yet_valid"},
		{"hostname", x509.HostnameError{}, "tls_hostname"},
		{"timeout", context.DeadlineExceeded, "timeout"},
		{"canceled", context.Canceled, "canceled"},
		{"redirect", &url.Error{Op: "Get", URL: "https://private.invalid/token", Err: errors.New("stopped after 10 redirects")}, "redirect_limit"},
		{"connect", &net.OpError{Op: "dial", Err: errors.New("private address")}, "connect"},
		{"closed", io.ErrUnexpectedEOF, "connection_closed"},
		{"other", errors.New("private detail"), "transport_other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := wordpressRuntimeHTTPErrorCategory(test.err); got != test.want {
				t.Fatalf("category=%s want=%s", got, test.want)
			}
		})
	}
}
