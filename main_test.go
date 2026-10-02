package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestVersionLineIncludesVersionAndPlatform(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "v9.9.9"

	line := versionLine()
	for _, want := range []string{"vynl v9.9.9", runtime.Version(), runtime.GOOS, runtime.GOARCH} {
		if !strings.Contains(line, want) {
			t.Errorf("versionLine() = %q, missing %q", line, want)
		}
	}
}
