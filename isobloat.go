package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Low-risk ISO-tree bloat removal (the -keep-iso-extras opt-out).
//
// These are files on the installation media -- NOT inside install.wim -- whose
// removal cannot affect install, boot, or OOBE. They are additions on top of
// nano11builder.ps1's own steps, gated behind a keep flag (like
// -keep-defender-search) purely so a hypothetical regression stays bisectable;
// nothing here has ever been observed to break anything.
//
//   - netfx3 on-demand cab (~71 MB): the offline .NET Framework 3.5 payload
//     under sources\sxs. Removing it only means enabling the .NET 3.5 optional
//     feature later needs Windows Update instead of the local cab -- setup and
//     first boot never touch it.
//   - credits.htm/credits.txt (~18 MB): the license/credits text reachable
//     from Setup's "Legal" link. Cosmetic.
//   - CJK boot fonts (~27 MB across both font dirs): chs/cht/jpn/kor and the
//     Chinese/Japanese/Korean UI faces used by the Windows Boot Manager only
//     to render pre-boot text (boot menu, BitLocker recovery) in those
//     scripts. An en-US image never uses them. The Latin boot fonts
//     (segoe_slboot, segoen_slboot, segmono_boot, wgl4_boot) are KEPT -- those
//     do render the en-US boot UI, so they are load-bearing.
//
// memtest is deliberately left in place: it is tiny and the -grub-efi menu's
// memtest entry chainloads efi/microsoft/boot/memtest.efi from the media.

// cjkBootFonts are the font files removed from each of bootFontDirs. Everything
// not on this list (notably segoe*/segmono/wgl4) is kept.
var cjkBootFonts = []string{
	"chs_boot.ttf", "cht_boot.ttf", "jpn_boot.ttf", "kor_boot.ttf",
	"malgun_boot.ttf", "malgun_console.ttf", "malgunn_boot.ttf",
	"meiryo_boot.ttf", "meiryo_console.ttf", "meiryon_boot.ttf",
	"msjh_boot.ttf", "msjh_console.ttf", "msjhn_boot.ttf",
	"msyh_boot.ttf", "msyh_console.ttf", "msyhn_boot.ttf",
}

// bootFontDirs are the two trees carrying identical copies of the boot fonts:
// boot/ (BIOS) and efi/microsoft/boot/ (UEFI).
var bootFontDirs = []string{
	filepath.Join("boot", "fonts"),
	filepath.Join("efi", "microsoft", "boot", "fonts"),
}

// removeISOExtras deletes the low-risk scaffolding files described above from
// the extracted ISO tree in place, before authoring.
func removeISOExtras(dir string) error {
	var freed int64
	rm := func(path string) error {
		fi, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		freed += fi.Size()
		fmt.Printf("Removing non-essential media file: %s (%d bytes)\n",
			filepath.ToSlash(mustRel(dir, path)), fi.Size())
		return nil
	}

	// netfx3 offline cab (version suffix varies -> glob).
	cabs, err := filepath.Glob(filepath.Join(dir, "sources", "sxs", "*netfx3-ondemand*.cab"))
	if err != nil {
		return fmt.Errorf("glob netfx3 cab: %w", err)
	}
	for _, c := range cabs {
		if err := rm(c); err != nil {
			return err
		}
	}

	// credits.htm/txt in every locale dir under sources/.
	for _, name := range []string{"credits.htm", "credits.txt"} {
		matches, err := filepath.Glob(filepath.Join(dir, "sources", "*", name))
		if err != nil {
			return fmt.Errorf("glob %s: %w", name, err)
		}
		for _, m := range matches {
			if err := rm(m); err != nil {
				return err
			}
		}
	}

	// CJK boot fonts from both font trees.
	for _, fdir := range bootFontDirs {
		for _, font := range cjkBootFonts {
			if err := rm(filepath.Join(dir, fdir, font)); err != nil {
				return err
			}
		}
	}

	fmt.Printf("Removed %.1f MB of non-essential media files (-keep-iso-extras to keep them)\n", float64(freed)/1e6)
	return nil
}

// mustRel returns path relative to base, or path unchanged if that fails.
func mustRel(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return rel
	}
	return path
}
