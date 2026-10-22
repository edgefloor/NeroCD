package hostingruntimeprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func fixture(t *testing.T) *Environment {
	t.Helper()
	in := domain.HostingRuntimeEnvironmentRequest{Authority: domain.HostingAuthority{OperationID: "hop", RunID: "run_hop", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: strings.Repeat("a", 32), PlanHash: "sha256:" + strings.Repeat("b", 64), RuntimeConfiguration: domain.HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("c", 64)}}
	e, err := NewEnvironment(Metadata{Authority: in.Authority, DeliveryID: in.DeliveryID, PlanHash: in.PlanHash, RuntimeConfiguration: in.RuntimeConfiguration, RequestHash: RuntimeEnvironmentRequestHash("runner", in), Purpose: Purpose, UseUntil: time.Now().Add(time.Minute)}, []Entry{{Name: "APP", Value: []byte("private-value")}, {Name: "EMPTY", Value: nil}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestRuntimeEnvironmentAttemptBounds(t *testing.T) {
	e := fixture(t)
	m, err := e.Metadata()
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingRuntimeEnvironmentRequest{Authority: m.Authority, DeliveryID: m.DeliveryID, PlanHash: m.PlanHash, RuntimeConfiguration: m.RuntimeConfiguration}
	for _, attempt := range []int64{-1, 0, 1, math.MaxInt32, math.MaxInt32 + 1, 1<<32 + 1} {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			if int64(int(attempt)) != attempt {
				t.Skip("attempt is not representable by this architecture's int")
			}
			in.Authority.Attempt = int(attempt)
			valid := attempt >= 1 && attempt <= math.MaxInt32
			if got := ValidateRequest(in); (got == nil) != valid {
				t.Fatalf("attempt %d validation=%v valid=%v", attempt, got, valid)
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ReadRequest(bytes.NewReader(raw)); (err == nil) != valid {
				t.Fatalf("attempt %d request codec accepted=%v valid=%v", attempt, err == nil, valid)
			}
			metadata := m
			metadata.Authority = in.Authority
			owned, err := NewEnvironment(metadata, []Entry{})
			if owned != nil {
				owned.Close()
			}
			if (err == nil) != valid {
				t.Fatalf("attempt %d metadata accepted=%v valid=%v", attempt, err == nil, valid)
			}
		})
	}
}

func TestEnvironmentOwnershipCodecAndRedaction(t *testing.T) {
	e := fixture(t)
	metadata, _ := e.Metadata()
	input := []Entry{{Name: "COPY", Value: []byte("original")}}
	copyOwned, err := NewEnvironment(metadata, input)
	if err != nil {
		t.Fatal(err)
	}
	defer copyOwned.Close()
	input[0].Value[0] = 'X'
	if err = copyOwned.WithValues(func(entries []Entry) error {
		if string(entries[0].Value) != "original" {
			t.Fatal("constructor retained input alias")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := WriteEnvironment(&wire, e); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadEnvironment(&wire)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	var alias []byte
	if err = decoded.WithValues(func(entries []Entry) error {
		if len(entries) != 2 || string(entries[0].Value) != "private-value" || len(entries[1].Value) != 0 {
			t.Fatal("values changed")
		}
		alias = entries[0].Value
		for _, v := range []any{entries[0], &entries[0], decoded, *decoded} {
			raw, _ := json.Marshal(v)
			if string(raw) != "{}" || strings.Contains(fmt.Sprintf("%v %+v %#v", v, v, v), "private-value") {
				t.Fatal("ordinary serialization exposed values")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	decoded.Close()
	if !bytes.Equal(alias, make([]byte, len(alias))) {
		t.Fatal("owned bytes not cleared")
	}
	if _, err = decoded.Metadata(); err == nil {
		t.Fatal("closed metadata accessible")
	}
	if decoded.WithValues(func([]Entry) error { return nil }) == nil {
		t.Fatal("closed values accessible")
	}
}

func TestEnvironmentStrictWire(t *testing.T) {
	e := fixture(t)
	var out bytes.Buffer
	if err := WriteEnvironment(&out, e); err != nil {
		t.Fatal(err)
	}
	valid := out.String()
	cases := []string{
		valid + `{}`, strings.Replace(valid, `"purpose":`, `"purpose":"bad","purpose":`, 1), strings.Replace(valid, `"purpose":`, `"Purpose":`, 1), strings.Replace(valid, `"attempt":1`, `"attempt":1,"ATTEMPT":1`, 1), strings.Replace(valid, `"generation":1`, `"generation":1,"unknown":0`, 1), strings.Replace(valid, `"value":"cHJpdmF0ZS12YWx1ZQ=="`, `"value":null`, 1), strings.Replace(valid, `"name":"APP"`, `"name":"APP","name":"OTHER"`, 1), strings.Replace(valid, `"name":"APP"`, `"name":"\ud800"`, 1), strings.Replace(valid, `"name":"APP"`, "\"name\":\"\xff\"", 1), strings.Replace(valid, `"value":"cHJpdmF0ZS12YWx1ZQ=="`, `"value":"YQpi"`, 1), strings.Replace(valid, `"purpose":"hosting_container_environment"`, `"purpose":"other"`, 1), strings.Repeat(" ", MaxEnvironmentWireBytes+1),
	}
	for i, raw := range cases {
		got, err := ReadEnvironment(strings.NewReader(raw))
		if got != nil {
			got.Close()
		}
		if err != ErrInvalidMessage {
			t.Fatalf("case %d accepted or diagnostic escaped", i)
		}
	}
}

func TestEnvironmentBudgets(t *testing.T) {
	for _, value := range [][]byte{bytes.Repeat([]byte("a"), 4097), {0}, {'\r'}, {'\n'}, {0xff}} {
		if ValidateEntries([]Entry{{Name: "APP", Value: value}}) == nil {
			t.Fatal("invalid value accepted")
		}
	}
	for _, name := range []string{"NEROCD_KEY", "bad-name", "", "1BAD"} {
		if ValidateEntries([]Entry{{Name: name}}) == nil {
			t.Fatal("invalid name accepted")
		}
	}
	entries := make([]Entry, 64)
	for i := range entries {
		entries[i] = Entry{Name: fmt.Sprintf("APP_%d", i), Value: bytes.Repeat([]byte("a"), 4096)}
	}
	if ValidateEntries(entries) == nil {
		t.Fatal("complete environment budget ignored")
	}
	entries = entries[:63]
	if ValidateEntries(entries) != nil {
		t.Fatal("valid environment rejected")
	}
	e := fixture(t)
	m, _ := e.Metadata()
	large, err := NewEnvironment(m, entries)
	if err != nil {
		t.Fatal(err)
	}
	defer large.Close()
	var wire bytes.Buffer
	if err = WriteEnvironment(&wire, large); err != nil || wire.Len() <= MaxEnvironmentBytes {
		t.Fatal("base64 wire headroom unavailable", err)
	}
	decoded, err := ReadEnvironment(&wire)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Close()
}

func TestReadRuntimeRequestStrict(t *testing.T) {
	e := fixture(t)
	m, _ := e.Metadata()
	in := domain.HostingRuntimeEnvironmentRequest{Authority: m.Authority, DeliveryID: m.DeliveryID, PlanHash: m.PlanHash, RuntimeConfiguration: m.RuntimeConfiguration}
	raw, _ := json.Marshal(in)
	if _, err := ReadRequest(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(raw) + `{}`, strings.Replace(string(raw), `"authority":`, `"Authority":`, 1), strings.Replace(string(raw), `"attempt":1`, `"attempt":1,"attempt":2`, 1), strings.Replace(string(raw), `"generation":1`, `"generation":0`, 1)} {
		if _, err := ReadRequest(strings.NewReader(s)); err == nil {
			t.Fatal("ambiguous request accepted")
		}
	}
}
