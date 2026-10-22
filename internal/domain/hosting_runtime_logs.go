package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Runtime log limits bound demand, collector authority, plaintext and responses.
const (
	HostingRuntimeLogsCapability      = "hosting_runtime_logs_v1"
	HostingRuntimeLogDemandSeconds    = 30
	HostingRuntimeLogLeaseSeconds     = 10
	HostingRuntimeLogRetentionSeconds = 300
	HostingRuntimeLogBatchLimit       = 64
	HostingRuntimeLogMessageLimit     = 16 << 10
	HostingRuntimeLogByteLimit        = 256 << 10
	HostingRuntimeLogSessionBytes     = 1 << 20
	HostingRuntimeLogSessionRows      = 1000
	HostingRuntimeLogReadLimit        = 200
)

var runtimeLogRandomID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// HostingRuntimeLogOpen freezes a short admission deadline across retries.
type HostingRuntimeLogOpen struct {
	RequestID        string    `json:"request_id"`
	RequestExpiresAt time.Time `json:"request_expires_at"`
}

// Canonical validates the stable request identity; admission checks database time.
func (in HostingRuntimeLogOpen) Canonical() (HostingRuntimeLogOpen, error) {
	if !runtimeLogRandomID.MatchString(in.RequestID) || in.RequestExpiresAt.IsZero() || in.RequestExpiresAt.Year() < 1 || in.RequestExpiresAt.Year() > 9999 {
		return HostingRuntimeLogOpen{}, errors.New("invalid runtime log request")
	}
	in.RequestExpiresAt = in.RequestExpiresAt.UTC().Truncate(time.Microsecond)
	return in, nil
}

// HostingRuntimeLogTarget is the immutable settled container chosen by the server.
type HostingRuntimeLogTarget struct {
	ResourceID       string `json:"resource_id"`
	BindingID        string `json:"binding_id"`
	DestinationID    string `json:"destination_id"`
	ServerID         string `json:"server_id"`
	RunnerID         string `json:"runner_id"`
	OperationID      string `json:"operation_id"`
	DaemonID         string `json:"daemon_id"`
	ContainerID      string `json:"container_id"`
	ImageID          string `json:"image_id"`
	ReleaseHash      string `json:"release_hash"`
	PlanHash         string `json:"plan_hash"`
	RuntimeNamespace string `json:"runtime_namespace"`
}

// Validate bounds every exact container identity without accepting runtime selectors.
func (target HostingRuntimeLogTarget) Validate() error {
	for _, value := range []string{target.ResourceID, target.BindingID, target.DestinationID, target.ServerID, target.RunnerID, target.OperationID, target.DaemonID, target.ContainerID, target.RuntimeNamespace} {
		if !observationID(value, false) {
			return errors.New("invalid runtime log target identity")
		}
	}
	if !imageSHA256.MatchString(target.ImageID) || !imageSHA256.MatchString(target.ReleaseHash) || !imageSHA256.MatchString(target.PlanHash) {
		return errors.New("invalid runtime log target hash")
	}
	return nil
}

// HostingRuntimeLogSession labels sensitive cached output with its original target.
type HostingRuntimeLogSession struct {
	ID              string                  `json:"id"`
	RequestID       string                  `json:"request_id"`
	ResourceID      string                  `json:"resource_id"`
	ProjectID       string                  `json:"project_id"`
	ActorID         string                  `json:"actor_id"`
	Target          HostingRuntimeLogTarget `json:"target"`
	State           string                  `json:"state"`
	CreatedAt       time.Time               `json:"created_at"`
	DemandExpiresAt time.Time               `json:"demand_expires_at"`
	EndedAt         *time.Time              `json:"ended_at"`
	PurgeAfter      time.Time               `json:"purge_after"`
	ServerTime      time.Time               `json:"server_time"`
	Sensitive       bool                    `json:"sensitive"`
}

// HostingRuntimeLogClaimInput identifies one process-local collector instance.
type HostingRuntimeLogClaimInput struct {
	InstanceID     string `json:"instance_id"`
	AfterSessionID string `json:"after_session_id"`
	Limit          int    `json:"limit"`
}

// Validate bounds collector identity and discovery pagination.
func (in HostingRuntimeLogClaimInput) Validate() error {
	if !runtimeLogRandomID.MatchString(in.InstanceID) || !observationID(in.AfterSessionID, true) || in.Limit < 1 || in.Limit > 4 {
		return errors.New("invalid runtime log claim")
	}
	return nil
}

// HostingRuntimeLogClaim grants only a bounded lease on an already demanded target.
type HostingRuntimeLogClaim struct {
	SessionID       string                  `json:"session_id"`
	InstanceID      string                  `json:"instance_id"`
	CollectionEpoch string                  `json:"collection_epoch"`
	EpochExpiresAt  time.Time               `json:"epoch_expires_at"`
	DemandExpiresAt time.Time               `json:"demand_expires_at"`
	Target          HostingRuntimeLogTarget `json:"target"`
	NextSequence    int                     `json:"next_sequence"`
}

// HostingRuntimeLogClaims is a bounded server-selected sweep.
type HostingRuntimeLogClaims struct {
	Targets    []HostingRuntimeLogClaim `json:"targets"`
	NextCursor string                   `json:"next_cursor"`
	EndOfSweep bool                     `json:"end_of_sweep"`
}

// HostingRuntimeLogEvent is sensitive application plaintext, never audit metadata.
type HostingRuntimeLogEvent struct {
	Component        string     `json:"component"`
	Stream           string     `json:"stream"`
	Message          string     `json:"message"`
	SourceTimestamp  *time.Time `json:"source_timestamp,omitempty"`
	MessageTruncated bool       `json:"message_truncated,omitempty"`
}

// HostingRuntimeLogBatch binds publication to a process instance and collection epoch.
type HostingRuntimeLogBatch struct {
	SessionID        string                   `json:"session_id"`
	InstanceID       string                   `json:"instance_id"`
	CollectionEpoch  string                   `json:"collection_epoch"`
	BatchID          string                   `json:"batch_id"`
	ExpectedSequence int                      `json:"expected_sequence"`
	Events           []HostingRuntimeLogEvent `json:"events"`
	State            string                   `json:"state"`
}

// Canonical validates bounded closed event metadata and normalizes source timestamps.
func (in HostingRuntimeLogBatch) Canonical() (HostingRuntimeLogBatch, error) {
	if !observationID(in.SessionID, false) || !runtimeLogRandomID.MatchString(in.InstanceID) || !runtimeLogRandomID.MatchString(in.CollectionEpoch) || !runtimeLogRandomID.MatchString(in.BatchID) || in.ExpectedSequence < 0 || in.ExpectedSequence > math.MaxInt32-HostingRuntimeLogBatchLimit || in.Events == nil || len(in.Events) > HostingRuntimeLogBatchLimit {
		return HostingRuntimeLogBatch{}, errors.New("invalid runtime log batch identity")
	}
	if in.State != "following" && in.State != "disconnected" && in.State != "unsupported_driver" {
		return HostingRuntimeLogBatch{}, errors.New("invalid runtime log state")
	}
	out := in
	out.Events = append([]HostingRuntimeLogEvent{}, in.Events...)
	for i, event := range out.Events {
		if event.Component != "application" || event.Stream != "stdout" && event.Stream != "stderr" && event.Stream != "combined" || len(event.Message) > HostingRuntimeLogMessageLimit || !utf8.ValidString(event.Message) || strings.ContainsRune(event.Message, 0) {
			return HostingRuntimeLogBatch{}, errors.New("invalid runtime log event")
		}
		if event.SourceTimestamp != nil {
			stamp := event.SourceTimestamp.UTC().Truncate(time.Microsecond)
			if stamp.Year() < 1 || stamp.Year() > 9999 {
				return HostingRuntimeLogBatch{}, errors.New("invalid runtime log timestamp")
			}
			out.Events[i].SourceTimestamp = &stamp
		}
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > HostingRuntimeLogByteLimit {
		return HostingRuntimeLogBatch{}, errors.New("runtime log batch exceeds byte limit")
	}
	return out, nil
}

// HostingRuntimeLogAck contains no plaintext and never extends demand or a lease.
type HostingRuntimeLogAck struct {
	SessionID       string    `json:"session_id"`
	InstanceID      string    `json:"instance_id"`
	CollectionEpoch string    `json:"collection_epoch"`
	BatchID         string    `json:"batch_id"`
	BodyHash        string    `json:"body_hash"`
	FirstSequence   int       `json:"first_sequence"`
	LastSequence    int       `json:"last_sequence"`
	ReceivedAt      time.Time `json:"received_at"`
}

// HostingRuntimeLogItem records accepted server order, not exact Docker coverage.
type HostingRuntimeLogItem struct {
	Sequence int `json:"sequence"`
	HostingRuntimeLogEvent
	ReceivedAt time.Time `json:"received_at"`
}

// HostingRuntimeLogPage exposes bounded plaintext only to its authorized session actor.
type HostingRuntimeLogPage struct {
	Session                   HostingRuntimeLogSession `json:"session"`
	Items                     []HostingRuntimeLogItem  `json:"items"`
	NextSequence              int                      `json:"next_sequence"`
	EarliestAvailableSequence int                      `json:"earliest_available_sequence"`
	DeletedThroughSequence    int                      `json:"deleted_through_sequence"`
	Gap                       bool                     `json:"gap"`
	Truncated                 bool                     `json:"truncated"`
	Markers                   []string                 `json:"markers"`
	Sensitive                 bool                     `json:"sensitive"`
}

// DecodeHostingRuntimeLogsJSON rejects duplicates, aliases, nulls and unknown fields.
func DecodeHostingRuntimeLogsJSON(raw []byte, out any) error {
	if len(raw) > HostingRuntimeLogByteLimit || !validRuntimeJSONUnicode(raw) {
		return errors.New("invalid runtime log JSON")
	}
	var fields any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) (any, error)
	walk = func(depth int) (any, error) {
		if depth > 8 {
			return nil, errors.New("runtime log JSON too deep")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		if token == nil {
			return nil, errors.New("null runtime log field")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid runtime log key")
				}
				if _, exists := m[k]; exists {
					return nil, errors.New("duplicate runtime log key")
				}
				v, e := walk(depth + 1)
				if e != nil {
					return nil, e
				}
				m[k] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, errors.New("invalid runtime log object")
			}
			return m, nil
		case '[':
			values := []any{}
			for d.More() {
				v, e := walk(depth + 1)
				if e != nil {
					return nil, e
				}
				values = append(values, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, errors.New("invalid runtime log array")
			}
			return values, nil
		default:
			return nil, errors.New("invalid runtime log delimiter")
		}
	}
	var err error
	fields, err = walk(0)
	if err != nil {
		return err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing runtime log JSON")
	}
	if err = runtimeLogFields(fields, reflect.TypeOf(out)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func runtimeLogFields(value any, typ reflect.Type) error {
	if typ == nil {
		return errors.New("invalid runtime log DTO")
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[time.Time]() {
		return nil
	}
	if typ.Kind() == reflect.Slice {
		items, ok := value.([]any)
		if !ok {
			return errors.New("invalid runtime log array")
		}
		for _, item := range items {
			if err := runtimeLogFields(item, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return errors.New("invalid runtime log object")
	}
	allowed := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")
		key := tag[0]
		allowed[key] = true
		v, present := fields[key]
		optional := len(tag) > 1 && tag[1] == "omitempty"
		if !present && !optional {
			return errors.New("missing runtime log field")
		}
		if present {
			if err := runtimeLogFields(v, f.Type); err != nil {
				return err
			}
		}
	}
	for key := range fields {
		if !allowed[key] {
			return errors.New("unknown runtime log field")
		}
	}
	return nil
}
