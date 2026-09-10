package main

import (
	"testing"

	nano11go "github.com/Pandapip1/nano11-go"

	"github.com/Pandapip1/gowim/lzx"
)

// TestLzxPresetFlag covers the -lzx-preset flag.Value adapter: a valid name
// sets both the Options and the recorded name (used for the printed "LZX
// preset: %s" line), and an invalid one is rejected rather than silently
// falling back to a default.
func TestLzxPresetFlag(t *testing.T) {
	var opts lzx.Options
	name := "fast"
	f := lzxPresetFlag{opts: &opts, name: &name}

	if err := f.Set("balanced"); err != nil {
		t.Fatalf("Set(balanced): %v", err)
	}
	if name != "balanced" {
		t.Errorf("name = %q, want %q", name, "balanced")
	}
	if f.String() != "balanced" {
		t.Errorf("String() = %q, want %q", f.String(), "balanced")
	}

	if err := f.Set("not-a-real-preset"); err == nil {
		t.Fatal("Set(not-a-real-preset): want error, got nil")
	}
}

// TestWinREModeFlag covers the -winre-mode flag.Value adapter.
func TestWinREModeFlag(t *testing.T) {
	var mode nano11go.WinREMode
	f := winREModeFlag{&mode}

	if err := f.Set("donor-stub"); err != nil {
		t.Fatalf("Set(donor-stub): %v", err)
	}
	if mode != nano11go.WinREDonorStub {
		t.Errorf("mode = %v, want WinREDonorStub", mode)
	}
	if err := f.Set("keep"); err != nil {
		t.Fatalf("Set(keep): %v", err)
	}
	if mode != nano11go.WinREKeep {
		t.Errorf("mode = %v, want WinREKeep", mode)
	}
	if err := f.Set("bogus"); err == nil {
		t.Fatal("Set(bogus): want error, got nil")
	}
}
