package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const maxProxyBody = 2 << 20

type proxyState struct {
	mu                 sync.Mutex
	outage             bool
	dropNextEvent      bool
	dropNextCompletion bool
	dropNextEnrollment bool
	blockSecretAccess  bool
	secretBlockRelease chan struct{}
	blockedSecretCalls int
	activeSecretBlocks int
	lostEvents         int
	lostCompletions    int
	lostEnrollments    int
	requests           map[string]int
	trace              []string
}

type statusPayload struct {
	Outage          bool           `json:"outage"`
	SecretBlocked   bool           `json:"secret_access_blocked"`
	BlockedSecrets  int            `json:"blocked_secret_access_requests"`
	ActiveBlocks    int            `json:"active_secret_access_blocks"`
	LostEvents      int            `json:"lost_event_responses"`
	LostCompletions int            `json:"lost_completion_responses"`
	LostEnrollments int            `json:"lost_enrollment_responses"`
	Requests        map[string]int `json:"requests"`
	Trace           []string       `json:"trace"`
}

func main() {
	upstream, err := url.Parse(os.Getenv("UPSTREAM_URL"))
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		log.Fatal("UPSTREAM_URL must be an absolute URL")
	}
	state := &proxyState{requests: map[string]int{}}
	client := &http.Client{Timeout: 8 * time.Second}
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/__control/") {
			state.control(w, request)
			return
		}
		state.forward(w, request, upstream, client)
	})
	server := &http.Server{Addr: ":8081", Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func (s *proxyState) control(w http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch request.URL.Path {
	case "/__control/status":
		copyRequests := make(map[string]int, len(s.requests))
		for key, value := range s.requests {
			copyRequests[key] = value
		}
		_ = json.NewEncoder(w).Encode(statusPayload{Outage: s.outage, SecretBlocked: s.blockSecretAccess, BlockedSecrets: s.blockedSecretCalls, ActiveBlocks: s.activeSecretBlocks, LostEvents: s.lostEvents, LostCompletions: s.lostCompletions, LostEnrollments: s.lostEnrollments, Requests: copyRequests, Trace: append([]string(nil), s.trace...)})
	case "/__control/outage/on":
		s.outage = true
		s.addTraceLocked("control:outage-on")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/outage/off":
		s.outage = false
		s.addTraceLocked("control:outage-off")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/drop-event":
		s.dropNextEvent = true
		s.addTraceLocked("control:drop-event-armed")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/drop-completion":
		s.dropNextCompletion = true
		s.addTraceLocked("control:drop-completion-armed")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/drop-enrollment":
		s.dropNextEnrollment = true
		s.addTraceLocked("control:drop-enrollment-armed")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/secret-block/on":
		if !s.blockSecretAccess {
			s.blockSecretAccess = true
			s.secretBlockRelease = make(chan struct{})
		}
		s.addTraceLocked("control:secret-block-on")
		w.WriteHeader(http.StatusNoContent)
	case "/__control/secret-block/off":
		if s.blockSecretAccess {
			s.blockSecretAccess = false
			close(s.secretBlockRelease)
			s.secretBlockRelease = nil
		}
		s.addTraceLocked("control:secret-block-off")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, request)
	}
}

func (s *proxyState) forward(w http.ResponseWriter, request *http.Request, upstream *url.URL, client *http.Client) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxProxyBody+1))
	if err != nil || len(body) > maxProxyBody {
		http.Error(w, "request body rejected", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests[request.URL.Path]++
	if s.blockSecretAccess && request.URL.Path == "/api/v1/runners/secrets/access" {
		release := s.secretBlockRelease
		s.blockedSecretCalls++
		s.activeSecretBlocks++
		s.addTraceLocked("blocked-no-upstream:secret-access")
		s.mu.Unlock()
		released := false
		select {
		case <-request.Context().Done():
		case <-release:
			released = true
		}
		s.mu.Lock()
		s.activeSecretBlocks--
		if released {
			s.addTraceLocked("blocked-failed-no-upstream:secret-access")
		} else {
			s.addTraceLocked("blocked-client-gone:secret-access")
		}
		s.mu.Unlock()
		if released {
			http.Error(w, "secret access path unavailable", http.StatusBadGateway)
		}
		return
	}
	if s.outage && strings.HasPrefix(request.URL.Path, "/api/v1/runners/") {
		s.addTraceLocked("outage:" + request.URL.Path)
		s.mu.Unlock()
		http.Error(w, "runner API path unavailable", http.StatusBadGateway)
		return
	}
	s.mu.Unlock()

	target := *upstream
	target.Path = request.URL.Path
	target.RawQuery = request.URL.RawQuery
	forwarded, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "forward request failed", http.StatusBadGateway)
		return
	}
	forwarded.Header = request.Header.Clone()
	response, err := client.Do(forwarded)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxProxyBody+1))
	_ = response.Body.Close()
	if readErr != nil || len(responseBody) > maxProxyBody {
		http.Error(w, "upstream response rejected", http.StatusBadGateway)
		return
	}

	drop := false
	s.mu.Lock()
	switch request.URL.Path {
	case "/api/v1/runners/events/batch":
		if s.dropNextEvent && response.StatusCode < 400 {
			s.dropNextEvent = false
			s.lostEvents++
			drop = true
			s.addTraceLocked("committed-response-lost:events")
		}
	case "/api/v1/runners/complete":
		if s.dropNextCompletion && response.StatusCode < 400 {
			s.dropNextCompletion = false
			s.lostCompletions++
			drop = true
			s.addTraceLocked("committed-response-lost:completion")
		}
	case "/api/v1/runner-enrollments/consume":
		if s.dropNextEnrollment && response.StatusCode < 400 {
			s.dropNextEnrollment = false
			s.lostEnrollments++
			drop = true
			s.addTraceLocked("committed-response-lost:enrollment")
		}
	}
	if !drop {
		s.addTraceLocked(fmt.Sprintf("response:%s:%d", request.URL.Path, response.StatusCode))
	}
	s.mu.Unlock()
	if drop {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "cannot discard response", http.StatusBadGateway)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err == nil {
			_ = connection.Close()
		}
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (s *proxyState) addTraceLocked(event string) {
	s.trace = append(s.trace, time.Now().UTC().Format(time.RFC3339Nano)+" "+event)
	if len(s.trace) > 512 {
		s.trace = append([]string(nil), s.trace[len(s.trace)-512:]...)
	}
}
