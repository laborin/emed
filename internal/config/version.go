package config

import (
	"runtime/debug"
	"strings"
)

// AppName is the display name of the application.
const AppName = "Emed"

const devVersion = "dev"

// version is injected at build time by the Makefile and GoReleaser:
//
//	-ldflags "-X github.com/laborin/emed/internal/config.version=0.5.0"
var version string

// Version returns the version of the running binary, without the "v" prefix.
// When it was not injected at build time, it fallbacks to the module version
// recorded by `go install` and finally to "dev".
func Version() string {
	return resolveVersion(version, debug.ReadBuildInfo)
}

func resolveVersion(injected string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if v := normalizeVersion(injected); v != "" {
		return v
	}
	if info, ok := readBuildInfo(); ok {
		if v := normalizeVersion(info.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	return devVersion
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
