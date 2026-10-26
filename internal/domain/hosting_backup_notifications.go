package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidNotification = errors.New("invalid backup notification")

const NotificationMaxAttempts = 5

// NotificationChannel is safe for any project viewer. Targets and recipient
// identities are deliberately absent from the read model.
type NotificationChannel struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"project_id"`
	Name                string    `json:"name"`
	Type                string    `json:"type"`
	Enabled             bool      `json:"enabled"`
	Generation          int64     `json:"generation"`
	Events              []string  `json:"events"`
	Configured          bool      `json:"configured"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type NotificationChannelInput struct {
	RequestID          string   `json:"request_id"`
	ExpectedGeneration int64    `json:"expected_generation"`
	Name               string   `json:"name"`
	Type               string   `json:"type"`
	Enabled            bool     `json:"enabled"`
	Events             []string `json:"events"`
}

func (in NotificationChannelInput) Validate() error {
	if !notificationID(in.RequestID) || in.ExpectedGeneration < 0 || len(in.Name) < 1 || len(in.Name) > 80 || strings.TrimSpace(in.Name) != in.Name || strings.ContainsAny(in.Name, "\x00\r\n") || (in.Type != "email" && in.Type != "webhook") || len(in.Events) > 2 {
		return ErrInvalidNotification
	}
	seen := map[string]bool{}
	for _, e := range in.Events {
		if (e != "backup_failed" && e != "upload_failed") || seen[e] {
			return ErrInvalidNotification
		}
		seen[e] = true
	}
	return nil
}

type NotificationTestInput struct {
	RequestID          string `json:"request_id"`
	ExpectedGeneration int64  `json:"expected_generation"`
}

func (in NotificationTestInput) Validate() error {
	if !notificationID(in.RequestID) || in.ExpectedGeneration < 1 {
		return ErrInvalidNotification
	}
	return nil
}

// NotificationConfiguration is write-only. Encode is restricted to encryption
// and transport boundaries; ordinary JSON and formatting always redact it.
type NotificationConfiguration struct {
	WebhookURL       string   `json:"webhook_url,omitempty"`
	SMTPHost         string   `json:"smtp_host,omitempty"`
	SMTPPort         int      `json:"smtp_port,omitempty"`
	SMTPSecurity     string   `json:"smtp_security,omitempty"`
	SMTPUsername     string   `json:"smtp_username,omitempty"`
	SMTPPassword     string   `json:"smtp_password,omitempty"`
	FromAddress      string   `json:"from_address,omitempty"`
	RecipientUserIDs []string `json:"recipient_user_ids,omitempty"`
}
type notificationConfigurationJSON NotificationConfiguration

func (NotificationConfiguration) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[write-only notification configuration]")
}
func (NotificationConfiguration) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }
func (c *NotificationConfiguration) Encode() ([]byte, error) {
	return json.Marshal((*notificationConfigurationJSON)(c))
}
func DecodeNotificationConfiguration(raw []byte) (*NotificationConfiguration, error) {
	var c notificationConfigurationJSON
	if len(raw) > 16384 || DecodeHostingStorageProtocolJSON(raw, &c) != nil {
		return nil, ErrInvalidNotification
	}
	out := NotificationConfiguration(c)
	return &out, nil
}
func (c *NotificationConfiguration) Close() {
	if c != nil {
		c.WebhookURL = ""
		c.SMTPUsername = ""
		c.SMTPPassword = ""
		c.FromAddress = ""
		clear(c.RecipientUserIDs)
		c.RecipientUserIDs = nil
		c.SMTPHost = ""
	}
}
func (c *NotificationConfiguration) Validate(kind string) error {
	if c == nil {
		return ErrInvalidNotification
	}
	if kind == "webhook" {
		u, err := url.Parse(c.WebhookURL)
		if err != nil || len(c.WebhookURL) > 4096 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(c.WebhookURL, "\x00\r\n\\") || c.SMTPHost != "" || c.SMTPPort != 0 || c.SMTPSecurity != "" || c.SMTPUsername != "" || c.SMTPPassword != "" || c.FromAddress != "" || len(c.RecipientUserIDs) != 0 {
			return ErrInvalidNotification
		}
		if u.Port() != "" {
			n, err := strconv.Atoi(u.Port())
			if err != nil || n < 1 || n > 65535 {
				return ErrInvalidNotification
			}
		}
		return nil
	}
	if kind != "email" || c.WebhookURL != "" || len(c.SMTPHost) < 1 || len(c.SMTPHost) > 253 || strings.ContainsAny(c.SMTPHost, "\x00\r\n /\\@:") && net.ParseIP(c.SMTPHost) == nil || c.SMTPPort < 1 || c.SMTPPort > 65535 || (c.SMTPSecurity != "starttls" && c.SMTPSecurity != "tls") || len(c.SMTPUsername) > 256 || len(c.SMTPPassword) > 4096 || strings.ContainsAny(c.SMTPUsername+c.SMTPPassword, "\x00\r\n") || (c.SMTPUsername == "") != (c.SMTPPassword == "") || !NotificationEmail(c.FromAddress) || len(c.RecipientUserIDs) < 1 || len(c.RecipientUserIDs) > 50 {
		return ErrInvalidNotification
	}
	seen := map[string]bool{}
	for _, id := range c.RecipientUserIDs {
		if !notificationID(id) || seen[id] {
			return ErrInvalidNotification
		}
		seen[id] = true
	}
	return nil
}
func NotificationEmail(v string) bool {
	a, err := mail.ParseAddress(v)
	return err == nil && a.Address == v && len(v) <= 254 && !strings.ContainsAny(v, "\x00\r\n")
}
func notificationID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for i, c := range v {
		if i == 0 && (c == '_' || c == '-') {
			return false
		}
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

type NotificationAttempt struct {
	Number      int        `json:"number"`
	State       string     `json:"state"`
	FailureCode string     `json:"failure_code"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}
type NotificationDelivery struct {
	ID                 string                `json:"id"`
	EventID            string                `json:"event_id"`
	ProjectID          string                `json:"project_id"`
	ChannelID          string                `json:"channel_id"`
	ChannelGeneration  int64                 `json:"channel_generation"`
	Event              string                `json:"event"`
	SourceKind         string                `json:"source_kind"`
	SourceID           string                `json:"source_id"`
	SourceState        string                `json:"source_state"`
	State              string                `json:"state"`
	AttemptCount       int                   `json:"attempt_count"`
	MaxAttempts        int                   `json:"max_attempts"`
	FailureCode        string                `json:"failure_code"`
	LocalCopyAvailable bool                  `json:"local_copy_available"`
	RecoveryURL        string                `json:"recovery_url"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	Attempts           []NotificationAttempt `json:"attempts,omitempty"`
}

// NotificationPayload contains stable identities and safe failure classes only.
// RecoveryURL contains no credentials and confers no authority.
type NotificationPayload struct {
	EventID            string `json:"event_id"`
	DeliveryID         string `json:"delivery_id"`
	Event              string `json:"event"`
	Message            string `json:"message"`
	SourceKind         string `json:"source_kind"`
	SourceID           string `json:"source_id"`
	SourceState        string `json:"source_state"`
	FailureCode        string `json:"failure_code"`
	LocalCopyAvailable bool   `json:"local_copy_available"`
	RecoveryURL        string `json:"recovery_url"`
}

func NotificationSelected(events []string, event string) bool {
	return event == "test" || slices.Contains(events, event)
}
func NotificationMessage(event string, sourceState ...string) string {
	if len(sourceState) > 0 && sourceState[0] == "uncertain" {
		if event == "upload_failed" {
			return "Database backup succeeded locally, remote upload outcome uncertain; needs attention"
		}
		return "Database backup outcome uncertain; needs attention"
	}
	switch event {
	case "backup_failed":
		return "Database backup failed"
	case "upload_failed":
		return "Database backup succeeded locally, remote upload failed"
	default:
		return "Test backup notification"
	}
}
