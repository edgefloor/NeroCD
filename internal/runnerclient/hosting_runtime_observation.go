package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"nerocd/internal/domain"
	"net/http"
	"strings"
)

type observationResponse[T any] struct {
	value T
	limit int
}

func (r *observationResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, int64(r.limit+1)))
	if err != nil {
		return err
	}
	if len(raw) > r.limit {
		return errors.New("observation response exceeds limit")
	}
	if !strictObservationResponse(raw, r.value) {
		return errors.New("invalid observation response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&r.value); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("observation response requires one JSON document")
	}
	return nil
}

// HostingObservationTargets requests a bounded server-selected observation sweep.
func (c Client) HostingObservationTargets(ctx context.Context, in domain.HostingObservationTargetsInput, options ...ObservationTransport) (domain.HostingObservationTargets, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingObservationTargets{}, err
	}
	r := observationResponse[domain.HostingObservationTargets]{limit: 512 << 10}
	err := c.observationPost(ctx, "/api/v1/runners/hosting-observations/targets", in, &r, options)
	return r.value, err
}

// ReportHostingObservation submits a closed sample and returns its stable acknowledgment.
func (c Client) ReportHostingObservation(ctx context.Context, in domain.HostingRuntimeObservation, options ...ObservationTransport) (domain.HostingObservationAck, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingObservationAck{}, err
	}
	r := observationResponse[domain.HostingObservationAck]{limit: domain.HostingObservationJSONLimit}
	err := c.observationPost(ctx, "/api/v1/runners/hosting-observations/report", in, &r, options)
	if err == nil && (r.value.SampleID != in.SampleID || r.value.BodyHash != domain.HostingDocumentHash(in) || r.value.ReceivedAt.IsZero()) {
		return domain.HostingObservationAck{}, errors.New("observation acknowledgment does not match sample")
	}
	return r.value, err
}

// ObservationTransport permits explicit literal-loopback HTTP fixtures only.
type ObservationTransport struct{ TrustedLoopbackHTTP bool }

// ValidateObservationServer rejects remote cleartext and ambiguous controller URLs.
func ValidateObservationServer(server string, options ObservationTransport) error {
	if ValidateRuntimeEnvironmentServer(server, RuntimeEnvironmentTransport(options)) != nil {
		return errors.New("invalid hosting observation controller transport")
	}
	return nil
}
func (c Client) observationPost(ctx context.Context, path string, in, out any, options []ObservationTransport) error {
	if len(options) > 1 {
		return errors.New("invalid observation transport")
	}
	option := ObservationTransport{}
	if len(options) == 1 {
		option = options[0]
	}
	if err := ValidateObservationServer(c.Server, option); err != nil {
		return err
	}
	secured, closeTransport, err := c.observationHTTP()
	if err != nil {
		return err
	}
	defer closeTransport()
	return secured.post(ctx, path, in, out)
}

func (c Client) observationHTTP() (Client, func(), error) {
	original := c.HTTP
	if original == nil {
		original = http.DefaultClient
	}
	client := *original
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	base, ok := transport.(*http.Transport)
	if !ok || base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify {
		return c, nil, errors.New("invalid observation TLS transport")
	}
	clone := base.Clone()

	clone.DisableKeepAlives = true
	if strings.HasPrefix(c.Server, "http:") {
		clone.Proxy = nil
	}
	client.Transport = clone
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTP = &client
	return c, clone.CloseIdleConnections, nil
}
func observationObject(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(keys) {
		return false
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return false
		}
	}
	return true
}
func strictObservationResponse(raw []byte, value any) bool {
	if !observationUniqueJSON(json.NewDecoder(bytes.NewReader(raw))) {
		return false
	}
	switch value.(type) {
	case domain.HostingObservationTargets:
		if !observationObject(raw, "targets", "next_cursor", "end_of_sweep") {
			return false
		}
		var object struct {
			Targets []json.RawMessage `json:"targets"`
		}
		if json.Unmarshal(raw, &object) != nil {
			return false
		}
		for _, target := range object.Targets {
			var fields map[string]json.RawMessage
			if json.Unmarshal(target, &fields) != nil {
				return false
			}
			if storage, ok := fields["storage"]; ok {
				if !observationObject(storage, "volume_name", "mount_path") {
					return false
				}
				delete(fields, "storage")
				var err error
				target, err = json.Marshal(fields)
				if err != nil {
					return false
				}
			}
			if !observationObject(target, "sample_id", "issued_at", "expires_at", "resource_id", "binding_id", "destination_id", "server_id", "runner_id", "operation_id", "daemon_id", "container_id", "image_id", "release_hash", "plan_hash", "runtime_namespace", "container_port", "host_port", "readiness_path", "sample_timeout_seconds") {
				return false
			}
		}
		return true
	case domain.HostingObservationAck:
		return observationObject(raw, "sample_id", "body_hash", "received_at")
	}
	return false
}
func observationUniqueJSON(d *json.Decoder) bool {
	token, err := d.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			token, err = d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !observationUniqueJSON(d) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !observationUniqueJSON(d) {
				return false
			}
		}
	default:
		return false
	}
	_, err = d.Token()
	return err == nil
}
