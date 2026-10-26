package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type notificationHTTPRepository struct {
	*archiveHTTPRepository
	channel  domain.NotificationChannel
	delivery domain.NotificationDelivery
	secret   *store.NotificationSecret
}

func (r *notificationHTTPRepository) SaveNotificationChannel(ctx context.Context, id, project, actor, existing string, in domain.NotificationChannelInput, secret *store.NotificationSecret, _ []string, _ store.NotificationSecretCompare) (domain.NotificationChannel, error) {
	r.calls++
	if err := r.authorize(ctx, actor, domain.RoleOwner); err != nil {
		return r.channel, err
	}
	if existing != "" {
		id = existing
	}
	r.channel.ID = id
	r.channel.Generation = in.ExpectedGeneration + 1
	r.channel.Enabled = in.Enabled
	r.channel.Events = in.Events
	r.secret = secret
	return r.channel, nil
}
func (r *notificationHTTPRepository) ListNotificationChannels(ctx context.Context, actor, project string) ([]domain.NotificationChannel, error) {
	r.calls++
	return []domain.NotificationChannel{r.channel}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *notificationHTTPRepository) TestNotificationChannel(ctx context.Context, id, project, actor, channel string, in domain.NotificationTestInput) (domain.NotificationDelivery, error) {
	r.calls++
	return r.delivery, r.authorize(ctx, actor, domain.RoleOwner)
}
func (r *notificationHTTPRepository) ListNotificationDeliveries(ctx context.Context, actor, project string) ([]domain.NotificationDelivery, error) {
	r.calls++
	return []domain.NotificationDelivery{r.delivery}, r.authorize(ctx, actor, domain.RoleViewer)
}
func (r *notificationHTTPRepository) GetNotificationDelivery(ctx context.Context, actor, project, id string) (domain.NotificationDelivery, error) {
	r.calls++
	return r.delivery, r.authorize(ctx, actor, domain.RoleViewer)
}
func TestNotificationHTTPPermissionsEncryptionAndClosedDocuments(t *testing.T) {
	server, original, owner, viewer, runner := archiveHTTPFixture(t)
	repo := &notificationHTTPRepository{archiveHTTPRepository: original}
	mem := repo.MemoryStore
	ring, err := secretcrypto.New("fixture", map[string][]byte{"fixture": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, SecretKeyring: ring, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	now := time.Now().UTC()
	repo.channel = domain.NotificationChannel{ID: "channel", ProjectID: original.resource.ProjectID, Name: "alerts", Type: "webhook", Enabled: true, Generation: 1, Events: []string{"backup_failed"}, Configured: true, CreatedAt: now, UpdatedAt: now}
	repo.delivery = domain.NotificationDelivery{ID: "delivery", EventID: "event", ProjectID: original.resource.ProjectID, ChannelID: "channel", ChannelGeneration: 1, Event: "test", SourceKind: "test", SourceID: "test_id", SourceState: "test", State: "pending", MaxAttempts: 5, RecoveryURL: "/projects?project_id=" + original.resource.ProjectID, CreatedAt: now, UpdatedAt: now}
	path := "/api/v1/projects/" + original.resource.ProjectID + "/notification-channels"
	body := `{"request_id":"request","expected_generation":0,"name":"alerts","type":"webhook","enabled":true,"events":["backup_failed"],"configuration":{"webhook_url":"https://example.com/private?token=canary_endpoint"}}`
	archiveHTTPCall(t, server, "POST", path, "", body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, runner, body, "application/json", 401)
	archiveHTTPCall(t, server, "POST", path, viewer, body, "application/json", 403)
	archiveHTTPCall(t, server, "POST", path, owner, body, "application/json", 200)
	if repo.secret == nil || strings.Contains(string(repo.secret.Envelope.Data), "canary_endpoint") {
		t.Fatal("plaintext reached persistence")
	}
	raw, err := ring.Open(secretcrypto.Binding{ProjectID: repo.channel.ProjectID, SecretID: repo.secret.ID, Version: repo.secret.Version}, repo.secret.Envelope)
	if err != nil || !strings.Contains(string(raw), "canary_endpoint") {
		t.Fatal("credential encryption binding lost")
	}
	clear(raw)
	archiveHTTPCall(t, server, "GET", path, viewer, "", "", 200)
	archiveHTTPCall(t, server, "GET", strings.Replace(path, "notification-channels", "notification-deliveries", 1), viewer, "", "", 200)
	archiveHTTPCall(t, server, "POST", path+"/channel/tests", viewer, `{"request_id":"test_request","expected_generation":1}`, "application/json", 403)
	archiveHTTPCall(t, server, "POST", path+"/channel/tests", owner, `{"request_id":"test_request","expected_generation":1}`, "application/json", 202)
	before := repo.calls
	for _, bad := range []string{strings.Replace(body, `"enabled":true,`, "", 1), strings.Replace(body, `"enabled":true`, `"enabled":null`, 1), strings.Replace(body, `"expected_generation":0,`, "", 1), strings.Replace(body, `"events":["backup_failed"]`, `"events":null`, 1), strings.Replace(body, `"webhook_url":`, `"unknown":`, 1), strings.Replace(body, `"name":"alerts"`, `"name":"alerts","name":"other"`, 1), strings.Replace(body, "https://example.com", "http://example.com", 1)} {
		archiveHTTPCall(t, server, "POST", path, owner, bad, "application/json", 400)
	}
	if repo.calls != before {
		t.Fatal("invalid document reached persistence")
	}
	view, _ := json.Marshal(repo.channel)
	if strings.Contains(string(view), "endpoint") || strings.Contains(string(view), "recipient") || strings.Contains(string(view), "smtp") {
		t.Fatal("private fields in metadata")
	}
}
func TestNotificationUnavailableExplainsProtectedOrigin(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, store.ErrBackupNotificationsUnavailable)
	if w.Code != 503 {
		t.Fatal("missing notification configuration was not unavailable", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["code"] != "backup_notifications_unavailable" || !strings.Contains(body["error"], "NEROCD_PUBLIC_ORIGIN") {
		t.Fatal("missing safe configuration instruction", body, err)
	}
}

func TestNotificationOpenAPIMatchesReadModels(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"NotificationChannel": domain.NotificationChannel{}, "NotificationDelivery": domain.NotificationDelivery{}, "NotificationAttempt": domain.NotificationAttempt{}} {
		typ := reflect.TypeOf(value)
		schema := doc.Components.Schemas[name].Value
		for i := 0; i < typ.NumField(); i++ {
			field := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if schema.Properties[field] == nil {
				t.Fatalf("%s missing %s", name, field)
			}
		}
		if len(schema.Properties) != typ.NumField() {
			t.Fatal("schema includes extra fields", name)
		}
	}
}
