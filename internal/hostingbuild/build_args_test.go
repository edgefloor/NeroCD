package hostingbuild

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildArgumentsOwnedIdentityAndArgv(t *testing.T) {
	original := settingRequest("/checkout", Dockerfile)
	original.BuildArgs = map[string]string{"Z": "first\nsecond\r\nthird", "A": "$(touch /not-executed) ' spaced = value", "EMPTY": ""}
	owned := ownedBuildRequest(original)
	before, err := requestIdentity(owned, Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	original.BuildArgs["Z"] = "changed"
	after, err := requestIdentity(owned, Config{}, "")
	if err != nil || before != after {
		t.Fatal("caller mutation changed request identity", err)
	}
	changed, _ := requestIdentity(original, Config{}, "")
	if changed == before {
		t.Fatal("arguments missing from request identity")
	}
	argv := owned.buildxArguments("builder", DockerfileFrontend, "/checkout", "Dockerfile", "/export")
	var arguments []string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--build-arg" {
			arguments = append(arguments, argv[i+1])
		}
	}
	want := []string{"BUILDKIT_SYNTAX=" + DockerfileFrontend, "A=$(touch /not-executed) ' spaced = value", "EMPTY=", "Z=first\nsecond\r\nthird"}
	if !reflect.DeepEqual(arguments, want) || argv[len(argv)-1] != "/checkout" {
		t.Fatalf("literal argv altered: %q", argv)
	}
	legacy := settingRequest("/checkout", Dockerfile)
	nilHash, _ := requestIdentity(legacy, Config{}, "")
	legacy.BuildArgs = map[string]string{}
	emptyHash, _ := requestIdentity(legacy, Config{}, "")
	if nilHash != emptyHash {
		t.Fatal("empty args changed legacy identity")
	}
	for _, name := range []string{"BUILDKIT_SYNTAX", "buildkit_inline_cache", "BUILDX_TEST", "HTTP_PROXY", "TARGETPLATFORM", "NEROCD_TOKEN", "invalid-name"} {
		bad := settingRequest("/checkout", Dockerfile)
		bad.BuildArgs = map[string]string{name: "value"}
		if bad.validateSettings() == nil {
			t.Fatalf("reserved/invalid argument %s accepted", name)
		}
	}
	for _, value := range []string{"nul\x00", strings.Repeat("x", 4097), string([]byte{0xff})} {
		bad := settingRequest("/checkout", Dockerfile)
		bad.BuildArgs = map[string]string{"APP": value}
		if bad.validateSettings() == nil {
			t.Fatal("invalid value accepted")
		}
	}
	for _, strategy := range []Strategy{Static, Railpack} {
		bad := settingRequest("/checkout", strategy)
		bad.BuildArgs = map[string]string{"APP": "value"}
		if bad.validateSettings() == nil {
			t.Fatal("arguments widened other strategies")
		}
	}
}
