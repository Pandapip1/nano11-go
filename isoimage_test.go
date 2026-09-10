package nano11go

import "testing"

// TestBuildISORequiresOut confirms BuildISO's own validation (mirroring the
// CLI's "-iso-out is required when -iso-dir is given" behavior) fires before
// any filesystem work against Dir, so this needs no real extracted-ISO tree
// fixture.
func TestBuildISORequiresOut(t *testing.T) {
	err := BuildISO(ISOOptions{Dir: "/nonexistent/iso-tree"})
	if err == nil {
		t.Fatal("BuildISO with empty Out: want error, got nil")
	}
}
