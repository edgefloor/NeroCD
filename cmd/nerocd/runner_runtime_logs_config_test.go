package main

import (
	"nerocd/internal/domain"
	"testing"
)

func TestRunnerRuntimeLogsOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		enabled, execute, once, specified bool
		complete                          string
		caps                              []string
		ok                                bool
	}{
		{name: "observer only", enabled: true, execute: true, caps: []string{"shell"}, ok: true},
		{name: "explicit observer", enabled: true, execute: true, specified: true, caps: []string{domain.HostingRuntimeLogsCapability}, ok: true},
		{name: "explicit shell", enabled: true, execute: true, specified: true, caps: []string{"shell"}},
		{name: "no execute", enabled: true},
		{name: "once", enabled: true, execute: true, once: true},
		{name: "complete", enabled: true, execute: true, complete: "succeeded"},
		{name: "missing flag", execute: true, caps: []string{domain.HostingRuntimeLogsCapability}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps, err := runnerRuntimeLogsCapabilities(tc.enabled, tc.execute, tc.once, tc.complete, tc.specified, tc.caps)
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
			if tc.ok {
				for _, c := range caps {
					if c == "shell" || c == domain.HostingBuildTaskType {
						t.Fatal("observer inherited process capability")
					}
				}
			}
		})
	}
}
