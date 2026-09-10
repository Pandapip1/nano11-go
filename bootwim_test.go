package nano11go

import "testing"

// TestShrinkBootWimMissingBootWim exercises ShrinkBootWim's error path when
// bootWimPath does not exist -- the same cheap, fixture-free approach as
// TestDebloatMissingWim, since a real boot.wim is a multi-hundred-MB fixture
// this repo does not carry.
func TestShrinkBootWimMissingBootWim(t *testing.T) {
	err := ShrinkBootWim("/nonexistent/path/boot.wim", "/tmp/out-boot.wim", BootWimOptions{})
	if err == nil {
		t.Fatal("ShrinkBootWim with a nonexistent -boot-wim: want error, got nil")
	}
}
