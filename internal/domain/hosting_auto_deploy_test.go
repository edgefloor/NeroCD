package domain

import (
	"fmt"
	"testing"
)

func autoConfigurationFixture() HostingAutoDeployConfiguration {
	return HostingAutoDeployConfiguration{Mode: HostingAutoDeployMode, ExpectedHealthyOperationID: "hop_manual", ExpectedLoadedProxyOperationID: "pop_manual", ExpectedLoadedGeneration: 2, ContainerPort: 80, Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 5}, Routes: []HostingRouteSwitchSelection{{RouteID: "route_b", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "hop_manual"}, {RouteID: "route_a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_manual"}}}
}
func TestHostingAutoDeployConfiguration(t *testing.T) {
	in := autoConfigurationFixture()
	out, err := CanonicalHostingAutoDeployConfiguration(in)
	if err != nil || out.Routes[0].RouteID != "route_a" || in.Routes[0].RouteID != "route_b" {
		t.Fatal("canonical copy", err)
	}
	out.Routes[0].ExpectedVersion = 99
	if in.Routes[1].ExpectedVersion != 1 {
		t.Fatal("aliased owner selectors")
	}
	tests := map[string]func(*HostingAutoDeployConfiguration){"protected": func(c *HostingAutoDeployConfiguration) { c.Mode = "automatic_protected" }, "unsupported": func(c *HostingAutoDeployConfiguration) { c.Mode = "anything" }, "runtime": func(c *HostingAutoDeployConfiguration) { c.ExpectedRuntimeConfigurationGeneration = -1 }, "bootstrap": func(c *HostingAutoDeployConfiguration) { c.ExpectedHealthyOperationID = "" }, "proxy": func(c *HostingAutoDeployConfiguration) { c.ExpectedLoadedProxyOperationID = "" }, "port": func(c *HostingAutoDeployConfiguration) { c.ContainerPort = 0 }, "readiness": func(c *HostingAutoDeployConfiguration) { c.Readiness.Path = "https://external.invalid/" }, "duplicate": func(c *HostingAutoDeployConfiguration) { c.Routes[1] = c.Routes[0] }, "empty": func(c *HostingAutoDeployConfiguration) { c.Routes = nil }}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			c := autoConfigurationFixture()
			change(&c)
			if _, err := CanonicalHostingAutoDeployConfiguration(c); err == nil {
				t.Fatal("invalid review accepted")
			}
		})
	}
	for _, n := range []int{HostingProxyMaxRoutes, HostingProxyMaxRoutes + 1} {
		c := autoConfigurationFixture()
		c.Routes = nil
		for i := 0; i < n; i++ {
			c.Routes = append(c.Routes, HostingRouteSwitchSelection{RouteID: fmt.Sprintf("route_%02d", i), ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_manual"})
		}
		_, err := CanonicalHostingAutoDeployConfiguration(c)
		if (err == nil) != (n == HostingProxyMaxRoutes) {
			t.Fatal("existing route bound", n, err)
		}
	}
}
