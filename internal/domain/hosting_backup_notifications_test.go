package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNotificationConfigurationClosedAndWriteOnly(t *testing.T) {
	c, err := DecodeNotificationConfiguration([]byte(`{"webhook_url":"https://example.com/private?token=canary"}`))
	if err != nil || c.Validate("webhook") != nil {
		t.Fatal("valid configuration rejected", err)
	}
	defer c.Close()
	raw, _ := json.Marshal(c)
	if string(raw) != "{}" || strings.Contains(fmt.Sprintf("%+v", c), "canary") {
		t.Fatal("configuration exposed by ordinary encoding")
	}
	sealedInput, _ := c.Encode()
	defer clear(sealedInput)
	if !strings.Contains(string(sealedInput), "canary") {
		t.Fatal("encryption boundary lost configuration")
	}
	for _, input := range []string{`null`, `{"webhook_url":"https://example.com","webhook_url":"https://other.com"}`, `{"webhook_url":"https://example.com","recipient":"x"}`, `{"smtp_password":"a","SMTP_PASSWORD":"b"}`} {
		if _, err = DecodeNotificationConfiguration([]byte(input)); err == nil {
			t.Fatal("open/duplicate document accepted")
		}
	}
	for _, endpoint := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/#secret", "https://example.com:0", "https://example.com/\r\nX:canary"} {
		bad := &NotificationConfiguration{WebhookURL: endpoint}
		if bad.Validate("webhook") == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
func TestNotificationEmailValidationAndSelectors(t *testing.T) {
	c := &NotificationConfiguration{SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPSecurity: "starttls", SMTPUsername: "user", SMTPPassword: "canary", FromAddress: "sender@example.com", RecipientUserIDs: []string{"usr_member"}}
	if c.Validate("email") != nil {
		t.Fatal("valid email rejected")
	}
	for _, change := range []func(*NotificationConfiguration){func(c *NotificationConfiguration) { c.FromAddress = "sender@example.com\r\nBcc:x@example.com" }, func(c *NotificationConfiguration) { c.SMTPSecurity = "none" }, func(c *NotificationConfiguration) { c.SMTPPassword = "" }, func(c *NotificationConfiguration) { c.RecipientUserIDs = []string{"usr_member", "usr_member"} }, func(c *NotificationConfiguration) { c.WebhookURL = "https://example.com" }} {
		copy := *c
		change(&copy)
		if copy.Validate("email") == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if !NotificationSelected([]string{"backup_failed", "upload_failed"}, "upload_failed") || NotificationSelected([]string{"backup_failed"}, "upload_failed") || !NotificationSelected(nil, "test") {
		t.Fatal("selector behavior changed")
	}
	for _, event := range []string{"backup_failed", "upload_failed"} {
		message := NotificationMessage(event, "uncertain")
		if !strings.Contains(message, "uncertain") {
			t.Fatal("uncertainty asserted failure")
		}
	}
	in := NotificationChannelInput{RequestID: "request", Name: "backup alerts", Type: "webhook", Events: []string{"backup_failed", "backup_failed"}}
	if in.Validate() == nil {
		t.Fatal("duplicate selector accepted")
	}
}

func TestNotificationSourceMessage(t *testing.T) {
	for _, event := range []string{"backup_failed", "upload_failed", "test"} {
		for _, state := range []string{"failed", "uncertain"} {
			if NotificationSourceMessage(event, "backup", state) != NotificationMessage(event, state) {
				t.Fatal("database wording changed")
			}
		}
	}
	if got := NotificationSourceMessage("backup_failed", "storage_backup", "uncertain"); got != "Storage backup outcome uncertain; needs attention" {
		t.Fatal(got)
	}
	if got := NotificationSourceMessage("upload_failed", "storage_transfer", "failed"); got != "Storage backup succeeded locally, remote upload failed" {
		t.Fatal(got)
	}
}
