package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionInfoIncludesVersionCommitAndGoVersion(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.26.2",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234def"},
			{Key: "vcs.modified", Value: "true"},
		},
	}

	out := versionInfo("v1.2.3", "2026-06-22T00:00:00Z", bi)

	for _, want := range []string{"v1.2.3", "abc1234def", "go1.26.2", "2026-06-22T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("versionInfo output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "dirty") {
		t.Errorf("versionInfo output should flag a modified build as dirty:\n%s", out)
	}
}

func TestVersionInfoEmptyVersionFallsBackToDev(t *testing.T) {
	out := versionInfo("", "", &debug.BuildInfo{GoVersion: "go1.26.2"})

	if !strings.Contains(out, "dev") {
		t.Errorf("empty version should fall back to %q:\n%s", "dev", out)
	}
}

func TestVersionInfoCleanBuildIsNotDirty(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.26.2",
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234def"},
			{Key: "vcs.modified", Value: "false"},
		},
	}

	out := versionInfo("v1.2.3", "", bi)

	if strings.Contains(out, "dirty") {
		t.Errorf("clean build should not be flagged dirty:\n%s", out)
	}
	if !strings.Contains(out, "abc1234def") {
		t.Errorf("clean build should still report the commit:\n%s", out)
	}
}

func TestVersionInfoHandlesNilBuildInfo(t *testing.T) {
	out := versionInfo("v1.2.3", "", nil)

	if !strings.Contains(out, "v1.2.3") {
		t.Errorf("version must still print without build info:\n%s", out)
	}
}
