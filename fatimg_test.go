package main

import (
	"bytes"
	"os"
	"testing"
)

// TestBuildGrubFATImageStructure checks the FAT16 invariants and, when a real
// a1ive GRUB EFI is available at NANO11_GRUB_EFI, writes the resulting boot
// image to NANO11_GRUB_FAT_OUT so it can be verified with mdir/mtype and booted.
func TestBuildGrubFATImageStructure(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x12}, 1500*1024) // ~6 MiB
	if src := os.Getenv("NANO11_GRUB_EFI"); src != "" {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read NANO11_GRUB_EFI: %v", err)
		}
		payload = b
	}

	img, err := buildGrubFATImage(payload)
	if err != nil {
		t.Fatalf("buildGrubFATImage: %v", err)
	}
	if len(img)%fatBytesPerSector != 0 {
		t.Fatalf("image size %d not sector-aligned", len(img))
	}
	if img[510] != 0x55 || img[511] != 0xAA {
		t.Fatalf("missing 0x55AA boot signature")
	}
	if got := string(img[54:62]); got != "FAT16   " {
		t.Fatalf("filesystem type = %q, want %q", got, "FAT16   ")
	}
	if img[0] != 0xEB {
		t.Fatalf("missing JMP at start of boot sector")
	}

	if out := os.Getenv("NANO11_GRUB_FAT_OUT"); out != "" {
		if err := os.WriteFile(out, img, 0o644); err != nil {
			t.Fatalf("write NANO11_GRUB_FAT_OUT: %v", err)
		}
		t.Logf("wrote %d-byte FAT image to %s", len(img), out)
	}
}
