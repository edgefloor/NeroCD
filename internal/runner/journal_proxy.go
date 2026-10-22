package runner

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// JournalProxy binds nonsecret proxy evidence to its exact immutable attempt.
// Operator paths, certificate keys and bearer credentials never belong here.
type JournalProxy struct {
	ID          string                              `json:"id"`
	Attempt     AttemptIdentity                     `json:"attempt_authority"`
	OperationID string                              `json:"operation_id"`
	Phase       string                              `json:"phase"`
	Plan        domain.HostingProxyPlan             `json:"plan"`
	Preparation domain.HostingProxyPreparation      `json:"preparation"`
	Prepared    *domain.HostingProxyPreparedRequest `json:"prepared,omitempty"`
	Result      *domain.HostingProxyCompleteRequest `json:"result,omitempty"`
	CreatedAt   time.Time                           `json:"created_at"`
}

// AppendProxy persists evidence before admission, effects, or receipt delivery.
func (j *AttemptJournal) AppendProxy(entry JournalProxy) (JournalProxy, error) {
	if err := validateJournalProxy(entry); err != nil {
		return JournalProxy{}, err
	}
	entry = cloneJournalProxy(entry)
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := json.Marshal(entry)
	if err != nil {
		return JournalProxy{}, err
	}
	for _, existing := range j.state.Proxy {
		if existing.ID != entry.ID {
			continue
		}
		old, err := json.Marshal(existing)
		if err != nil {
			return JournalProxy{}, err
		}
		if !bytes.Equal(raw, old) {
			return JournalProxy{}, ErrJournalConflict
		}
		return cloneJournalProxy(existing), nil
	}
	version := j.state.Version
	if j.state.Version < 4 {
		j.state.Version = 4
	}
	j.state.Proxy = append(j.state.Proxy, entry)
	if err = validateJournalState(j.state); err == nil {
		err = j.persistLocked()
	}
	if err != nil {
		j.state.Proxy = j.state.Proxy[:len(j.state.Proxy)-1]
		j.state.Version = version
		return JournalProxy{}, err
	}
	return cloneJournalProxy(entry), nil
}

// AckProxy removes evidence only after the caller verifies its server receipt.
// Intent acknowledgment additionally requires settled, non-restored ownership.
func (j *AttemptJournal) AckProxy(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	previous := j.state.Proxy
	kept := make([]JournalProxy, 0, len(previous))
	for _, entry := range previous {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	j.state.Proxy = kept
	if err := j.persistLocked(); err != nil {
		j.state.Proxy = previous
		return err
	}
	return nil
}

func cloneJournalProxy(entry JournalProxy) JournalProxy {
	encoded, _ := json.Marshal(entry) // Validated typed fields have no custom marshalers.
	var copy JournalProxy
	_ = json.Unmarshal(encoded, &copy)
	return copy
}

func cloneProxyEntries(entries []JournalProxy) []JournalProxy {
	copy := make([]JournalProxy, len(entries))
	for i, entry := range entries {
		copy[i] = cloneJournalProxy(entry)
	}
	return copy
}

func proxyJournalDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && strings.ToLower(value) == value && err == nil
}

func validateJournalProxy(entry JournalProxy) error {
	if entry.ID == "" || len(entry.ID) > 128 || entry.OperationID == "" || len(entry.OperationID) > 128 || entry.CreatedAt.IsZero() || entry.Attempt.RunID != "proxy_"+entry.OperationID {
		return errors.New("proxy journal requires stable identity and timestamp")
	}
	if err := validateAttemptIdentity(entry.Attempt); err != nil {
		return err
	}
	if err := domain.ValidateHostingProxyPlan(entry.Plan); err != nil {
		return err
	}
	p := entry.Preparation
	if p.PlanHash != entry.Plan.Hash || p.DaemonID == "" || len(p.DaemonID) > 128 || strings.ContainsAny(p.DaemonID, "\x00\r\n\t ") || entry.Plan.DaemonID != "" && entry.Plan.DaemonID != p.DaemonID || !proxyJournalDigest(p.EngineHash) || !proxyJournalDigest(p.StaticHash) {
		return errors.New("proxy journal preparation identity changed")
	}
	a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
	switch entry.Phase {
	case "prepared":
		if entry.Prepared == nil || entry.Result != nil || entry.Prepared.ReceiptID != entry.ID || entry.Prepared.HostingAuthority != a || entry.Prepared.HostingProxyPreparation != p {
			return errors.New("invalid proxy preparation receipt")
		}
	case "apply_intent":
		if entry.Prepared != nil || entry.Result != nil {
			return errors.New("invalid proxy apply intent")
		}
	case "result":
		if entry.Result == nil || entry.Prepared != nil || entry.Result.HostingAuthority != a || entry.Result.ReceiptID != entry.ID {
			return errors.New("invalid proxy result receipt")
		}
		result := entry.Result.Result
		if result.PlanHash != entry.Plan.Hash || result.EngineHash != p.EngineHash || result.DaemonID != p.DaemonID || result.LoadedGeneration < 0 || result.LoadedGeneration > entry.Plan.Snapshot.Generation || len(result.Routes) > domain.HostingProxyMaxRoutes {
			return errors.New("proxy result identity changed")
		}
		if result.Outcome != "applied" && result.Outcome != "rolled_back" && result.Outcome != "refused" && result.Outcome != "uncertain" {
			return errors.New("unknown proxy result outcome")
		}
		if result.ContainerID != "" && !proxyJournalDigest(result.ContainerID) {
			return errors.New("invalid proxy container identity")
		}
		targets := make(map[string]string, len(entry.Plan.Snapshot.Routes))
		for _, route := range entry.Plan.Snapshot.Routes {
			targets[route.RouteID] = route.Hostname
		}
		seen := make(map[string]bool, len(result.Routes))
		for _, route := range result.Routes {
			certificate := route.Certificate
			if targets[route.RouteID] == "" || seen[route.RouteID] || certificate.Hostname != targets[route.RouteID] || len(certificate.Issuer) > 1024 || (certificate.Status != "pending" && certificate.Status != "trusted") || certificate.Fingerprint != "" && !proxyJournalDigest(certificate.Fingerprint) || route.HTTPSReady && (!route.HTTPReady || certificate.Status != "trusted" || !proxyJournalDigest(certificate.Fingerprint) || certificate.ExpiresAt.IsZero()) {
				return errors.New("invalid proxy route observation")
			}
			// Historical committed results remain replayable after certificate
			// expiry. Fresh report authority and validity are checked by the server.
			seen[route.RouteID] = true
		}
	default:
		return errors.New("unknown proxy journal phase")
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > 96<<10 {
		return errors.New("proxy journal document exceeds limit")
	}
	return nil
}
