package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

// The version a binary reports is what a bug report is filed against, so the
// precedence has to be exact. The bug this covers: the source default was a
// hardcoded constant that went stale, so `go install module@v0.9.0` — which
// passes no -ldflags — reported "0.8.0".
func TestBuildStampsPrecedence(t *testing.T) {
	tests := []struct {
		name                           string
		ldVersion, ldCommit, ldTime    string
		info                           *debug.BuildInfo
		wantVer, wantCommit, wantBuilt string
	}{
		{
			name:      "ldflags win over everything",
			ldVersion: "1.2.3", ldCommit: "abc1234", ldTime: "2026-01-01T00:00:00Z",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "v9.9.9"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeefcafe"}},
			},
			wantVer: "1.2.3", wantCommit: "abc1234", wantBuilt: "2026-01-01T00:00:00Z",
		},
		{
			name:    "go install module@v0.9.0 supplies the version",
			info:    &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}},
			wantVer: "0.9.0", wantCommit: "unknown", wantBuilt: "unknown",
		},
		{
			name: "VCS stamp supplies commit and time for a plain go build",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "fa84e95763b5886cb32b2c257a16d85912624166"},
					{Key: "vcs.time", Value: "2026-11-28T01:57:05Z"},
				},
			},
			wantVer: "dev", wantCommit: "fa84e95", wantBuilt: "2026-11-28T01:57:05Z",
		},
		{
			name:    "(devel) is not a version",
			info:    &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			wantVer: "dev", wantCommit: "unknown", wantBuilt: "unknown",
		},
		{
			name:    "empty version string is not a version",
			info:    &debug.BuildInfo{Main: debug.Module{Version: ""}},
			wantVer: "dev", wantCommit: "unknown", wantBuilt: "unknown",
		},
		{
			name:    "no build info at all still says something honest",
			info:    nil,
			wantVer: "dev", wantCommit: "unknown", wantBuilt: "unknown",
		},
		{
			name:       "short revision is passed through unchanged",
			info:       &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc1234"}}},
			wantVer:    "dev",
			wantCommit: "abc1234",
			wantBuilt:  "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotVer, gotCommit, gotBuilt := buildStamps(tc.ldVersion, tc.ldCommit, tc.ldTime, tc.info)
			if gotVer != tc.wantVer {
				t.Errorf("version = %q, want %q", gotVer, tc.wantVer)
			}
			if gotCommit != tc.wantCommit {
				t.Errorf("commit = %q, want %q", gotCommit, tc.wantCommit)
			}
			if gotBuilt != tc.wantBuilt {
				t.Errorf("built = %q, want %q", gotBuilt, tc.wantBuilt)
			}
		})
	}
}

// A build with no stamps must never report an empty version: "vireo-mcp " in a
// bug report is worse than "vireo-mcp dev".
func TestResolveBuildInfoNeverEmpty(t *testing.T) {
	ver, commit, built := resolveBuildInfo()
	for name, v := range map[string]string{"version": ver, "commit": commit, "built": built} {
		if strings.TrimSpace(v) == "" {
			t.Errorf("%s is empty", name)
		}
	}
}

// The release path must keep working now that the defaults are empty: an
// explicit ldflags stamp is the only thing a tag build sets, and it has to
// survive the resolver untouched.
func TestLdflagsStampSurvivesResolver(t *testing.T) {
	old := []string{version, buildCommit, buildTime}
	defer func() { version, buildCommit, buildTime = old[0], old[1], old[2] }()

	version, buildCommit, buildTime = "0.99.0", "cafe123", "2026-12-01T00:00:00Z"

	ver, commit, built := resolveBuildInfo()
	if ver != "0.99.0" || commit != "cafe123" || built != "2026-12-01T00:00:00Z" {
		t.Fatalf("resolver altered an ldflags stamp: %q %q %q", ver, commit, built)
	}
	if got := serverVersion(); got != "0.99.0" {
		t.Errorf("serverVersion() = %q, want 0.99.0", got)
	}
}
