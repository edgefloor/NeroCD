package hostingdatabaseprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func credentialFixture(t *testing.T) (Metadata, []Credential) {
	t.Helper()
	in := domain.HostingDatabaseCredentialRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), Mode: "deploy"}
	recipe, err := domain.HostingDatabaseRecipeFor("postgresql", domain.HostingDatabasePostgresRecipe)
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]Credential, 2)
	for i := range recipe.Credentials {
		recipe.Credentials[i].SecretID = domain.HostingDatabaseSecretID("resource", recipe.Credentials[i].Role)
		recipe.Credentials[i].Version = 1
		entries[i] = Credential{Reference: recipe.Credentials[i], Value: []byte(strings.Repeat("A", 43))}
	}
	return Metadata{RunnerID: "runner", Authority: in.Authority, DeliveryID: in.DeliveryID, Mode: in.Mode, PlanHash: in.PlanHash, Database: domain.HostingDatabaseExecutionRef{Engine: recipe.Engine, RecipeVersion: recipe.Version, ConfigurationHash: in.PlanHash}, RequestHash: RequestHash("runner", in), Purpose: Purpose, UseUntil: time.Now().Add(time.Minute), References: recipe.Credentials}, entries
}

func TestCredentialsOwnershipCodecAndRedaction(t *testing.T) {
	m, entries := credentialFixture(t)
	c, err := NewCredentials(m, entries)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	entries[0].Value[0] = 'B'
	m.References[0].Username = "mutated"
	got, err := c.Metadata()
	if err != nil || got.References[0].Username != "postgres" {
		t.Fatal("metadata aliases caller")
	}
	got.References[0].Username = "mutated"
	var raw bytes.Buffer
	if err = WriteCredentials(&raw, c); err != nil {
		t.Fatal(err)
	}
	defer clear(raw.Bytes())
	decoded, err := ReadCredentials(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	var borrowed []byte
	if err = decoded.WithValues(func(v []Credential) error {
		borrowed = v[0].Value
		if v[0].Value[0] != 'A' {
			t.Fatal("value aliases caller")
		}
		for _, x := range []any{*decoded, v[0]} {
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				if strings.Contains(fmt.Sprintf(verb, x), strings.Repeat("A", 20)) {
					t.Fatal("format leak")
				}
			}
			b, e := json.Marshal(x)
			if e != nil || string(b) != "{}" {
				t.Fatal("ordinary JSON leak")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	decoded.Close()
	if !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("Close retained value")
	}
	if _, err = decoded.Metadata(); err == nil {
		t.Fatal("closed metadata available")
	}
	if WriteCredentials(&raw, decoded) == nil {
		t.Fatal("closed value encoded")
	}
}

func TestCredentialsRejectAmbiguousWire(t *testing.T) {
	m, entries := credentialFixture(t)
	c, err := NewCredentials(m, entries)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var b bytes.Buffer
	if WriteCredentials(&b, c) != nil {
		t.Fatal("encode")
	}
	raw := b.String()
	for _, bad := range []string{raw + "{}", strings.Replace(raw, `"purpose":`, `"unknown":1,"purpose":`, 1), strings.Replace(raw, `"purpose":`, `"purpose":"other","purpose":`, 1), strings.Replace(raw, `"runner_id":`, `"Runner_ID":`, 1), strings.Replace(raw, `"value":`, `"Value":`, 1), strings.Replace(raw, `"value":"`, `"value":"\\ud800`, 1), strings.Replace(raw, `"entries":[`, `"entries":[null,`, 1), strings.Replace(raw, `"reference":`, `"reference":null,"reference":`, 1), strings.Replace(raw, `"version":1`, `"version":null`, 1), strings.Repeat("x", MaxWireBytes+1)} {
		if got, e := ReadCredentials(strings.NewReader(bad)); e == nil {
			got.Close()
			t.Fatal("ambiguous response accepted")
		}
	}
}

func TestCredentialRequestClosedJSON(t *testing.T) {
	m, _ := credentialFixture(t)
	in := domain.HostingDatabaseCredentialRequest{Authority: m.Authority, DeliveryID: m.DeliveryID, Mode: m.Mode, PlanHash: m.PlanHash}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	if got, e := ReadRequest(bytes.NewReader(b)); e != nil || got != in {
		t.Fatal("request roundtrip", e)
	}
	for _, bad := range []string{raw + "{}", strings.Replace(raw, `"mode":`, `"mode":"observe","mode":`, 1), strings.Replace(raw, `"mode":`, `"Mode":`, 1), strings.Replace(raw, `"fence":`, `"extra":1,"fence":`, 1), strings.Replace(raw, `"delivery_id":`, `"secret_id":"other","delivery_id":`, 1), strings.Replace(raw, `"attempt":1`, `"attempt":null`, 1), strings.Repeat("x", 4097)} {
		if _, e := ReadRequest(strings.NewReader(bad)); e == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
}
