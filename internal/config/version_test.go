package config

import (
	"runtime/debug"
	"testing"
)

func buildInfoWith(version string, ok bool) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		if !ok {
			return nil, false
		}
		return &debug.BuildInfo{Main: debug.Module{Version: version}}, true
	}
}

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name      string
		injected  string
		buildInfo func() (*debug.BuildInfo, bool)
		want      string
	}{
		{"injected wins", "0.5.0", buildInfoWith("v0.4.0", true), "0.5.0"},
		{"injected v prefix is removed", "v0.5.0-3-gabc123", buildInfoWith("", true), "0.5.0-3-gabc123"},
		{"go install module version", "", buildInfoWith("v0.6.1", true), "0.6.1"},
		{"local devel build", "", buildInfoWith("(devel)", true), "dev"},
		{"no build info", "", buildInfoWith("", false), "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.injected, tt.buildInfo); got != tt.want {
				t.Errorf("resolveVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
