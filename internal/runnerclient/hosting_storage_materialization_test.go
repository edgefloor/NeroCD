package runnerclient

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func storageClientFixture() (domain.HostingStorageCreateRequest, domain.HostingStorageCreateAck, domain.HostingStorageMaterializedRequest, domain.HostingStorageMaterializedAck) {
	id, name := domain.HostingStorageIdentity("binding")
	storage := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: name, MountPath: "/app/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actual_creator", CreatedAt: time.Now().UTC()}
	storage.Hash = storage.DocumentHash()
	a := domain.HostingAuthority{OperationID: "origin", RunID: "run", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	create := domain.HostingStorageCreateRequest{HostingAuthority: a, StorageRef: storage.Reference(), PlanHash: "sha256:" + strings.Repeat("a", 64), CreationNonce: domain.HostingStorageCreationNonce(storage.Reference(), a.OperationID, "daemon")}
	origin := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: a.OperationID, CreationPlanHash: create.PlanHash, CreationNonce: create.CreationNonce, DaemonID: "daemon"}
	origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
	ack := domain.HostingStorageCreateAck{Storage: storage, Materialization: origin, CreatePermitted: true}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: "receipt", PlanHash: create.PlanHash, StorageRef: storage.Reference(), CreationOperationID: a.OperationID, CreationPlanHash: create.PlanHash, CreationNonce: create.CreationNonce, DaemonID: "daemon", VolumeName: name, Driver: "local", Scope: "local", Labels: maps.Clone(origin.Labels)}
	receipt := domain.HostingReceipt{ID: proof.ReceiptID, BodyHash: domain.HostingDocumentHash(proof), CreatedAt: time.Now().UTC()}
	origin.State, origin.MaterializedReceipt = "materialized", &receipt
	return create, ack, proof, domain.HostingStorageMaterializedAck{Materialization: origin, Receipt: receipt}
}

func TestStorageClientExactMetadataAndReceiptIdentity(t *testing.T) {
	create, ack, proof, ready := storageClientFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer runner_token" {
			t.Error("storage protocol lost dedicated bearer")
		}
		switch r.URL.Path {
		case "/api/v1/runners/hosting/storage-create-begin":
			var in domain.HostingStorageCreateRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in != create {
				t.Error("creation tuple changed")
			}
			_ = json.NewEncoder(w).Encode(ack)
		case "/api/v1/runners/hosting/storage-materialized":
			var in domain.HostingStorageMaterializedRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || domain.HostingDocumentHash(in) != domain.HostingDocumentHash(proof) {
				t.Error("materialization receipt changed")
			}
			_ = json.NewEncoder(w).Encode(ready)
		default:
			t.Error("unexpected storage protocol path")
		}
	}))
	defer server.Close()
	client := New(server.URL, "runner_token")
	got, err := client.HostingStorageCreateBegin(t.Context(), create)
	if err != nil || got.Storage != ack.Storage || !got.CreatePermitted {
		t.Fatalf("actual metadata transport failed: %+v %v", got, err)
	}
	for range 2 {
		got, err := client.HostingStorageMaterialized(t.Context(), proof)
		if err != nil || got.Receipt != ready.Receipt {
			t.Fatalf("exact receipt replay changed: %+v %v", got, err)
		}
	}
}

func TestStorageClientRejectsMalformedResponsesAndStaleAuthority(t *testing.T) {
	create, ack, proof, ready := storageClientFixture()
	ackRaw, _ := json.Marshal(ack)
	readyRaw, _ := json.Marshal(ready)
	for name, response := range map[string]string{
		"trailing":          string(ackRaw) + `{}`,
		"duplicate":         strings.Replace(string(ackRaw), `"create_permitted":true`, `"create_permitted":true,"create_permitted":false`, 1),
		"alias":             strings.Replace(string(ackRaw), `"create_permitted":true`, `"Create_Permitted":true`, 1),
		"unknown":           strings.Replace(string(ackRaw), `"create_permitted":true`, `"create_permitted":true,"reusable_permission":true`, 1),
		"invented_metadata": strings.Replace(string(ackRaw), `"mount_path":"/app/uploads"`, `"mount_path":"/other"`, 1),
		"missing_metadata":  `{"materialization":{},"create_permitted":true}`,
		"oversized":         strings.Repeat(" ", domain.HostingStorageProtocolJSONLimit+1) + string(ackRaw),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			if _, err := New(server.URL, "token").HostingStorageCreateBegin(t.Context(), create); err == nil {
				t.Fatal("malformed creation acknowledgment accepted")
			}
		})
	}
	for name, body := range map[string]string{"receipt_hash": strings.ReplaceAll(string(readyRaw), ready.Receipt.BodyHash, "sha256:"+strings.Repeat("0", 64)), "origin": strings.Replace(string(readyRaw), `"creation_operation_id":"origin"`, `"creation_operation_id":"other"`, 1)} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			if _, err := New(server.URL, "token").HostingStorageMaterialized(t.Context(), proof); err == nil {
				t.Fatal("forged materialization acknowledgment accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer server.Close()
	if _, err := New(server.URL, "token").HostingStorageMaterialized(t.Context(), proof); err == nil {
		t.Fatal("stale authority response was accepted")
	} else {
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusConflict {
			t.Fatal("stale authority conflict was hidden", err)
		}
	}
}
