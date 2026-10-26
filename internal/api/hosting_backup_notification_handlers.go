package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"nerocd/internal/domain"
)

func hostingNotificationPath(path string) bool {
	return strings.Contains(path, "/notification-channels") || strings.Contains(path, "/notification-deliveries")
}
func (s *Server) notificationChannels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		v, e := s.app.ListNotificationChannels(r.Context(), r.PathValue("project_id"))
		proxyResponse(w, 200, v, e)
		return
	}
	var wire struct {
		domain.NotificationChannelInput
		ExpectedGeneration *int64          `json:"expected_generation"`
		Enabled            *bool           `json:"enabled"`
		Events             *[]string       `json:"events"`
		Configuration      json.RawMessage `json:"configuration,omitempty"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 24577))
	defer clear(raw)
	if err != nil || len(raw) > 24576 || domain.DecodeHostingStorageProtocolJSON(raw, &wire) != nil || wire.ExpectedGeneration == nil || wire.Enabled == nil || wire.Events == nil {
		writeErrorEnvelope(w, 400, "invalid_request", "invalid notification channel document")
		return
	}
	wire.NotificationChannelInput.ExpectedGeneration = *wire.ExpectedGeneration
	wire.NotificationChannelInput.Enabled = *wire.Enabled
	wire.NotificationChannelInput.Events = *wire.Events
	var c *domain.NotificationConfiguration
	if wire.Configuration != nil {
		c, err = domain.DecodeNotificationConfiguration(wire.Configuration)
		clear(wire.Configuration)
		wire.Configuration = nil
		if err != nil {
			writeErrorEnvelope(w, 400, "invalid_request", "invalid notification configuration")
			return
		}
		defer c.Close()
	}
	v, e := s.app.SaveNotificationChannel(r.Context(), r.PathValue("project_id"), r.PathValue("channel_id"), wire.NotificationChannelInput, c)
	proxyResponse(w, 200, v, e)
}
func (s *Server) testNotificationChannel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !databaseBackupParameters(w, r) {
		return
	}
	var in domain.NotificationTestInput
	if !decodeDatabaseBackup(w, r, &in) {
		return
	}
	v, e := s.app.TestNotificationChannel(r.Context(), r.PathValue("project_id"), r.PathValue("channel_id"), in)
	proxyResponse(w, 202, v, e)
}
func (s *Server) notificationDeliveries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !databaseBackupParameters(w, r) {
		return
	}
	if r.PathValue("delivery_id") != "" {
		v, e := s.app.GetNotificationDelivery(r.Context(), r.PathValue("project_id"), r.PathValue("delivery_id"))
		proxyResponse(w, 200, v, e)
		return
	}
	v, e := s.app.ListNotificationDeliveries(r.Context(), r.PathValue("project_id"))
	proxyResponse(w, 200, v, e)
}
