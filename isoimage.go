package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Pandapip1/gowim/iso"
)

// ISO authoring: assemble the final bootable Windows installation image
// around this tool's debloated install.wim, using gowim's own ISO writer.
//
// This replaces rebuild-iso.sh, which shelled out to genisoimage. That
// script existed only because gowim did not implement ISO writing; its own
// header said so, and said it was "expected to be replaced by the library's
// own writer once that lands". It has (gowim's "ISO image creation
// subsystem": ECMA-119 core, the UDF 1.02 bridge, and El Torito), so the
// script is gone and everything it did lives here. The whole pipeline --
// reading install.wim, debloating it, rebuilding boot.wim, and authoring
// the bootable ISO -- is now Go code over gowim's libraries with no
// external tool anywhere in it.
//
// The genisoimage invocation being reproduced was:
//
//	genisoimage -iso-level 4 -udf -allow-limited-size -V <volid> \
//	  -b boot/etfsboot.com -no-emul-boot -boot-load-size 8 -boot-info-table \
//	  -eltorito-alt-boot \
//	  -e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot \
//	  -o <out> <dir>
//
// Two deliberate differences from it, both improvements:
//
//   - genisoimage's -boot-info-table MUTATES the boot image in the source
//     tree. eltorito.c's fill_boot_desc opens boot/etfsboot.com O_RDWR and
//     writes the 56-byte table over offsets 8..63 *on disk*, then copies the
//     file it just mutated, so an extracted tree stopped being byte-identical
//     to its source media after the first build (measured: retail
//     etfsboot.com hashes f425e135aac26b55..., the working trees under
//     /mnt/extra/nano11go-work carry two different already-patched copies).
//     gowim splices the table into the output stream only and never touches
//     the caller's file; iso.TestBootImageSourceIsNotModified asserts it, and
//     iso.TestCompareBootWithGenisoimage asserts genisoimage's mutation, so
//     the contrast is checked rather than assumed. The per-run
//     "cp -a isox isox_testN" convention that existed to give genisoimage a
//     scratch copy to scribble on is therefore no longer necessary for the
//     ISO step. This is safe because the table's checksum deliberately treats
//     the first 64 bytes -- exactly the bytes the table overwrites -- as
//     zero, so it is the same whether computed before or after the table
//     lands; measured identical (0x46eda81c) between the two producers.
//
//   - -iso-level 4 has no equivalent here: gowim does not implement the
//     ISO 9660:1999 Enhanced Volume Descriptor, so the ECMA-119 names come
//     out mangled where genisoimage's would be preserved. That costs
//     nothing for this workload. Windows installation media is read through
//     UDF, which carries the real names, and it must be: install.wim is
//     larger than the 4 GiB an ECMA-119 Data Length field can express on
//     the untrimmed editions, which is why iso.Options.LargeFilesUDFOnly
//     (oscdimg's own representation) is set below. Verified: 7z lists an
//     identical 1045-entry path list from the gowim-written image and from
//     the genisoimage one.
//
// efisys_noprompt.bin is Microsoft's own alternate UEFI El Torito boot image
// (it ships alongside efisys.bin in every real Windows ISO, wrapping
// cdboot_noprompt.efi instead of cdboot.efi) -- used here so Setup boots
// straight in, skipping the "Press any key to boot from CD or DVD..." prompt
// and its 5-second timing window. There is no equivalent no-prompt variant of
// the BIOS-mode boot sector boot/etfsboot.com, but that entry only matters for
// legacy BIOS boot; real UEFI firmware (and this project's qemu/OVMF test VMs)
// always takes the EFI entry.
const (
	biosBootImage = "boot/etfsboot.com"
	uefiBootImage = "efi/microsoft/boot/efisys_noprompt.bin"
)

// isoRootKeepList is nano11builder.ps1's own hardcoded $keepList, applied as
// its last step before it calls oscdimg: everything at the ISO root that is
// not one of these is deleted. On a real retail ISO that discards
// autorun.inf, a stray root-level bootmgfw.efi (the real one lives under
// efi/microsoft/boot/, not the root) and the support/ folder.
var isoRootKeepList = []string{
	"boot", "efi", "sources", "bootmgr", "bootmgr.efi", "setup.exe",
	"autounattend.xml",
}

// isoFlags collects the ISO-authoring stage's settings, mirroring the
// stageFlags convention used by the debloat stages.
type isoFlags struct {
	dir              string // extracted ISO tree to author from, modified in place
	out              string // output .iso path
	volID            string // ECMA-119 Volume Identifier / UDF volume label
	installWim       string // install.wim to place at sources/install.wim
	bootWim          string // boot.wim to place at sources/boot.wim
	skipAutounattend bool   // do not place autounattend.xml at the ISO root
}

// buildISO prepares the extracted ISO tree in place and writes a bootable
// image from it.
//
// The tree preparation steps are the ones nano11builder.ps1 performs on the
// media it is about to hand to oscdimg, and they are done here rather than
// left to the caller so that the whole "produce a bootable nano11 ISO"
// operation is one program.
func buildISO(f isoFlags) error {
	if f.out == "" {
		return fmt.Errorf("-iso-out is required when -iso-dir is given")
	}
	if f.installWim != "" {
		dst := filepath.Join(f.dir, "sources", "install.wim")
		fmt.Printf("Placing %s at sources/install.wim\n", f.installWim)
		if err := copyFileInto(f.installWim, dst); err != nil {
			return err
		}
	}
	if f.bootWim != "" {
		dst := filepath.Join(f.dir, "sources", "boot.wim")
		fmt.Printf("Placing %s at sources/boot.wim\n", f.bootWim)
		if err := copyFileInto(f.bootWim, dst); err != nil {
			return err
		}
	}

	// __chunk_data is an artifact of extracting the source ISO with 7z, not
	// part of the real media; it would otherwise be recorded into the image.
	if err := os.Remove(filepath.Join(f.dir, "__chunk_data")); err != nil && !os.IsNotExist(err) {
		return err
	}

	if !f.skipAutounattend {
		// nano11 keeps its own autounattend.xml at the ISO root (that is why
		// the keepList above names it) for unattended boot-time Setup,
		// separate from the copy installAutounattend places at
		// Windows\System32\Sysprep\autounattend.xml inside the image itself.
		// Both come from the same embedded copy, so the two can no longer
		// drift apart the way they could when the ISO copy was made by a
		// shell script pointed at a path.
		if err := os.WriteFile(filepath.Join(f.dir, "autounattend.xml"), autounattendXML, 0o644); err != nil {
			return err
		}
	}

	if err := cleanISORoot(f.dir); err != nil {
		return err
	}

	volID := f.volID
	if volID == "" {
		volID = "Nano11Go"
	}
	fmt.Printf("Authoring %s from %s (volume ID %q)...\n", f.out, f.dir, volID)
	start := time.Now()

	b := iso.New(&iso.Options{
		VolumeID: volID,
		// Level3 places no restriction on identifier length. It is not
		// -iso-level 4 (see the file comment), but it is as close as this
		// writer gets, and it is what the ECMA-119 view of a >4 GiB file
		// would need if LargeFilesUDFOnly were ever turned off.
		Level:     iso.Level3,
		Timestamp: time.Now().UTC(),
		// Windows installation media is a bridge volume and is read through
		// UDF; without this, install.wim could not be recorded at all above
		// 4 GiB.
		UDF:               true,
		LargeFilesUDFOnly: true,
		BootEntries: []iso.BootEntry{{
			// genisoimage: -b boot/etfsboot.com -no-emul-boot
			// -boot-load-size 8 -boot-info-table.
			ImagePath:     biosBootImage,
			Platform:      iso.BootPlatformX86,
			LoadSectors:   8,
			BootInfoTable: true,
		}, {
			// genisoimage: -eltorito-alt-boot
			// -e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot.
			// LoadSectors 0 means "derive from the file's size", which is
			// what genisoimage does without -boot-load-size: 2880 for the
			// 1 474 560-byte efisys_noprompt.bin.
			ImagePath: uefiBootImage,
			Platform:  iso.BootPlatformUEFI,
		}},
		// genisoimage names the catalog boot.catalog at the root, which is
		// iso.Options' zero value too; spelled out because the boot catalog
		// path is part of what the reference image was compared against.
		BootCatalogPath: "boot.catalog",
		// No PadSectors: genisoimage appends a 150-sector run-out for CD-R
		// drives whose read-ahead runs past the recorded area. Nothing here
		// is burned to CD-R, and ECMA-119 does not require it.
	})
	if err := b.AddTree("", f.dir); err != nil {
		return fmt.Errorf("read %s: %w", f.dir, err)
	}
	out, err := os.Create(f.out)
	if err != nil {
		return err
	}
	n, err := b.WriteTo(out)
	if err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", f.out, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d bytes) in %s\n", f.out, n, time.Since(start).Round(time.Millisecond))
	return nil
}

// cleanISORoot drops everything at the root of the extracted media that is
// not in isoRootKeepList, which is nano11builder.ps1's own final step before
// authoring.
func cleanISORoot(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(isoRootKeepList))
	for _, k := range isoRootKeepList {
		keep[k] = true
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		fmt.Printf("Removing non-essential file/folder from ISO root: %s\n", e.Name())
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFileInto copies src over dst, streaming rather than reading into
// memory: these are multi-gigabyte WIMs.
func copyFileInto(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Same path in and out is a no-op rather than a truncate-to-zero.
	if same, err := sameFile(src, dst); err != nil {
		return err
	} else if same {
		return nil
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}
