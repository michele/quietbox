package main

import (
	"runtime/debug"
	"testing"
)

func buildInfo(mainVersion string, settings map[string]string) *debug.BuildInfo {
	bi := &debug.BuildInfo{Main: debug.Module{Version: mainVersion}}
	for k, v := range settings {
		bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return bi
}

func TestDescribeVersion(t *testing.T) {
	cases := []struct {
		name                  string
		version, commit, date string
		bi                    *debug.BuildInfo
		want                  string
	}{
		{"release build", "v0.1.0", "1a2b3c4d5e6f", "2026-11-22", nil, "v0.1.0 (commit 1a2b3c4, built 2026-11-22)"},
		{"release build without a date", "v0.1.0", "1a2b3c4d5e6f", "", nil, "v0.1.0 (commit 1a2b3c4)"},
		{"release build with only a version", "v0.1.0", "", "", nil, "v0.1.0"},
		{"plain go build", "dev", "", "", nil, "dev"},
		{"go build inside a checkout", "dev", "", "", buildInfo("(devel)", map[string]string{"vcs.revision": "9f8e7d6c5b4a"}), "dev (commit 9f8e7d6)"},
		{"go install of a tag", "dev", "", "", buildInfo("v0.1.0", map[string]string{"vcs.revision": "9f8e7d6c5b4a"}), "v0.1.0 (commit 9f8e7d6)"},
		{"go install without vcs info", "dev", "", "", buildInfo("v0.1.0", nil), "v0.1.0"},
		{"ldflags win over build info", "v0.2.0", "aaaaaaa1", "2026-12-01", buildInfo("v0.1.0", map[string]string{"vcs.revision": "bbbbbbb2"}), "v0.2.0 (commit aaaaaaa, built 2026-12-01)"},
		{"a short commit is kept whole", "v0.1.0", "abc", "", nil, "v0.1.0 (commit abc)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describeVersion(c.version, c.commit, c.date, c.bi); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestVersionCommandAndFlag(t *testing.T) {
	want := "quietbox " + versionString() + "\n"
	for _, args := range [][]string{{"version"}, {"--version"}} {
		out, err := execRoot(t, args...)
		if err != nil || out != want {
			t.Errorf("%v: out=%q err=%v, want %q", args, out, err, want)
		}
	}
}

func TestVersionNeedsNoConfigOrPassword(t *testing.T) {
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file anywhere
	if _, err := execRoot(t, "version"); err != nil {
		t.Fatalf("version must not load the config: %v", err)
	}
}
