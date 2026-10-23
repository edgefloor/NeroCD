package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestHostingStorageMountPath(t *testing.T) {
	for _, value := range []string{"/data", "/app/uploads", "/var/lib/postgresql/data", "/processor", "/system", "/device", "/" + strings.Repeat("a", 511)} {
		if err := ValidateHostingStorageMountPath(value); err != nil {
			t.Fatalf("valid path %q: %v", value, err)
		}
	}
	for _, value := range []string{"/data" + string(rune(0)) + "upload", "/data" + string(rune(9)) + "upload", "/data" + string(rune(10)) + "upload", "", "/", "data", " /data", "/data/", "/data//upload", "/data/./upload", "/data/../upload", "/data\x00upload", "/data\nupload", "/data,readonly", "/data\"upload", "/data:ro", "/data=upload", "/data\\upload", "/proc", "/proc/self", "/sys", "/sys/kernel", "/dev", "/dev/shm", "/" + strings.Repeat("a", 512)} {
		if err := ValidateHostingStorageMountPath(value); err == nil {
			t.Fatalf("invalid path %q accepted", value)
		}
	}
}

func TestHostingStorageRequestContract(t *testing.T) {
	valid := HostingStorageInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, MountPath: "/uploads"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingStorageInput
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded != valid || decoded.Validate() != nil {
		t.Fatalf("valid JSON failed round trip: decoded=%+v err=%v", decoded, err)
	}
	for _, id := range []string{"", strings.Repeat("A", 32), strings.Repeat("a", 31), "00000000-0000-4000-8000-000000000001"} {
		in := valid
		in.RequestID = id
		if in.Validate() == nil {
			t.Fatalf("invalid request ID %q accepted", id)
		}
	}
	in := valid
	in.ExpectedResourceGeneration = 0
	if in.Validate() == nil {
		t.Fatal("missing resource generation accepted")
	}
	for _, raw := range []string{
		`{"request_id":"a","mount_path":"/uploads","volume_name":"external"}`,
		`{"request_id":"a","request_id":"b","mount_path":"/uploads"}`,
		`{"request_id":"a","mount_path":"/uploads"} {}`,
		`{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"Mount_Path":"/uploads"}`,
		`{"REQUEST_ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"mount_path":"/uploads"}`,
		`{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"mount_path":"/uploads","mount_path":"/different"}`,
		`{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1,"mount_path":null}`,
		`{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":1}`,
		`{"request_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_resource_generation":"1","mount_path":"/uploads"}`,
		`null`,
	} {
		var input HostingStorageInput
		if json.Unmarshal([]byte(raw), &input) == nil {
			t.Fatalf("ambiguous or server-owned input accepted: %s", raw)
		}
	}
}

func TestHostingStorageImmutableIdentityAndHash(t *testing.T) {
	id, volume := HostingStorageIdentity("binding_one")
	item := HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding_one", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/app/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now()}
	item.Hash = item.DocumentHash()
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := item.Reference(); got.StorageID != id || got.Generation != 1 || got.Hash != item.Hash {
		t.Fatal(got)
	}
	otherID, otherVolume := HostingStorageIdentity("binding_two")
	if otherID == id || otherVolume == volume {
		t.Fatal("distinct bindings share storage")
	}
	for _, mutate := range []func(*HostingStorage){
		func(s *HostingStorage) { s.ID = otherID }, func(s *HostingStorage) { s.ProjectID = "other" }, func(s *HostingStorage) { s.ResourceID = "other" }, func(s *HostingStorage) { s.BindingID = "other" }, func(s *HostingStorage) { s.DestinationID = "other" }, func(s *HostingStorage) { s.Kind = "external" }, func(s *HostingStorage) { s.Driver = "nfs" }, func(s *HostingStorage) { s.VolumeName = "adopted" }, func(s *HostingStorage) { s.MountPath = "/different" }, func(s *HostingStorage) { s.AccessMode = "read_only" }, func(s *HostingStorage) { s.Retention = "delete" }, func(s *HostingStorage) { s.Generation = 2 }, func(s *HostingStorage) { s.Hash = "sha256:" + strings.Repeat("0", 64) },
	} {
		changed := item
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatalf("changed immutable document accepted: %+v", changed)
		}
	}
	changed := item
	changed.CreatedBy = "second_actor"
	changed.CreatedAt = changed.CreatedAt.Add(time.Hour)
	if changed.DocumentHash() != item.Hash {
		t.Fatal("audit metadata changed runtime identity")
	}
}
