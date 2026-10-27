package main

import (
	"nerocd/internal/domain"
	"slices"
	"testing"
)

func TestRunnerObservationOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		enabled, execute, once, specified bool
		complete                          string
		caps                              []string
		ok                                bool
	}{
		{name: "observer only", enabled: true, execute: true, caps: []string{"shell"}, ok: true},
		{name: "explicit observer", enabled: true, execute: true, specified: true, caps: []string{domain.HostingObservationCapability}, ok: true},
		{name: "explicit storage observer", enabled: true, execute: true, specified: true, caps: []string{domain.HostingObservationCapability, domain.HostingStorageObservationCapability}, ok: true},
		{name: "missing storage flag", execute: true, caps: []string{domain.HostingStorageObservationCapability}},
		{name: "explicit shell", enabled: true, execute: true, specified: true, caps: []string{"shell"}},
		{name: "no execute", enabled: true},
		{name: "once", enabled: true, execute: true, once: true},
		{name: "complete", enabled: true, execute: true, complete: "succeeded"},
		{name: "missing flag", execute: true, caps: []string{domain.HostingObservationCapability}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps, err := runnerObservationCapabilities(tc.enabled, tc.execute, tc.once, tc.complete, tc.specified, tc.caps)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if tc.ok {
				for _, required := range []string{domain.HostingObservationCapability, domain.HostingStorageObservationCapability} {
					if !slices.Contains(caps, required) || slices.Contains(caps[slices.Index(caps, required)+1:], required) {
						t.Fatalf("capability must be advertised once: %s in %v", required, caps)
					}
				}
				for _, c := range caps {
					if c == "shell" || c == domain.HostingBuildTaskType {
						t.Fatal("observer inherited process capability")
					}
				}
			}
		})
	}
}
