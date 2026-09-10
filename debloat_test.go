package nano11go

import (
	"strings"
	"testing"
)

// TestDebloatMissingWim exercises Debloat's error path when wimPath does not
// exist, without needing a real (multi-GB) install.wim fixture: opening the
// source file is the very first thing Debloat does, so a nonexistent path is
// a fast, deterministic way to confirm the public entrypoint still wires
// through to that failure instead of panicking or silently succeeding.
func TestDebloatMissingWim(t *testing.T) {
	err := Debloat("/nonexistent/path/install.wim", "/tmp/out.wim", 1, DebloatOptions{})
	if err == nil {
		t.Fatal("Debloat with a nonexistent -wim: want error, got nil")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("Debloat error = %q, want it to mention the open failure", err.Error())
	}
}

// TestDebloatListImagesRequiresNoOutPath confirms imageIndex == 0 (list mode)
// does not require outPath, by checking the failure is about opening the
// (nonexistent) source file, not about a missing -out -- i.e. the outPath
// check in Debloat is correctly gated behind imageIndex != 0.
func TestDebloatListImagesRequiresNoOutPath(t *testing.T) {
	err := Debloat("/nonexistent/path/install.wim", "", 0, DebloatOptions{})
	if err == nil {
		t.Fatal("Debloat against a nonexistent -wim: want error, got nil")
	}
	if strings.Contains(err.Error(), "outPath") {
		t.Errorf("Debloat with imageIndex 0 should not complain about outPath, got: %v", err)
	}
}
