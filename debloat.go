// Package nano11go is a full, out-of-tree port of nano11builder.ps1
// (github.com/ntdevlabs/nano11) onto the gowim libraries
// (github.com/Pandapip1/gowim/*): it reproduces nano11's entire debloat
// pass against a real Windows install.wim edition -- AppX removal,
// servicing-package removal, the aggressive WinSxS wipe-to-allowlist,
// misc file deletions, and the full registry tweak set, including the
// steps nano11's own comments flag as breakage-prone -- using only
// gowim's Go packages: no DISM, no mounted image, no admin rights.
//
// It also authors the final bootable ISO itself, over gowim's iso package
// (ECMA-119 + the UDF 1.02 bridge + El Torito) -- see isoimage.go and
// ISOOptions/BuildISO. That step used to be a shell script calling
// genisoimage, rebuild-iso.sh, which existed only because gowim had not
// implemented ISO writing yet; it has, so the script is gone and there is
// now no external tool anywhere in the pipeline except 7z to *extract* the
// source ISO in the first place.
//
// Also NOT ported, both for the same reason (see gowim's TODO.md
// "CBS/servicing package subsystem" research entry): DISM's
// /Cleanup-Image /StartComponentCleanup /ResetBase, which nano11 calls
// twice. Its mechanism is undocumented COMPONENTS-hive-internal
// accounting reachable only through a live TrustedInstaller/CBS session --
// there is no offline equivalent to reimplement. Nothing is lost in terms
// of nano11's actual end result, though: the manual WinSxS
// wipe-to-allowlist (winsxs.go) that nano11 does as well achieves the
// same file-level size reduction ResetBase would; only ResetBase's own
// COMPONENTS-hive bookkeeping is skipped, which nano11's manual wipe
// already leaves permanently inconsistent anyway.
//
// Also NOT ported: the final install.wim -> install.esd recompression
// (gowim's LZMS encoder is a documented, unverified-against-an-independent-
// decoder risk -- see lzms/README.md).
//
// boot.wim handling (shrinking to just the setup image, its own
// requirement-bypass/BitLocker registry edits, and a non-en-US locale/
// WinSxS trim) IS ported/implemented -- see bootwim.go/BootWimOptions/
// ShrinkBootWim -- but is a separate opt-in step from the install.wim flow
// below, since it operates on an entirely different file. None of these
// steps reduce size when measured against the untouched source boot.wim
// (see TODO.md) -- but a registry-tweak pass forces a full re-encode of
// boot.wim regardless, and measured against that real alternative (a
// re-encode that keeps everything), both the image-drop and the locale/
// WinSxS trim are genuine, verified size wins. See TODO.md for the exact
// numbers and the QEMU/OVMF boot verification this was based on.
//
// Every WIM this tool writes is LZX-compressed through gowim's pure-Go
// encoder, and that encoder's speed/ratio tradeoff is selectable via
// DebloatOptions.LZX/BootWimOptions.LZX (see the gowim lzx package's own
// preset ladder). It is worth understanding before starting a run: a full
// pass re-encodes the whole image twice (the export pass, then the final
// write), so the preset, not the debloat work, is what sets the wall time.
package nano11go

import (
	"fmt"
	"os"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// DebloatOptions is the public options struct for Debloat, mirroring the
// nano11-go CLI's own stage flags so each risky debloat stage can be
// independently disabled/enabled from Go code exactly as it can from the
// command line -- e.g. to bisect which stage causes a boot-failure
// regression.
type DebloatOptions struct {
	SkipAppx        bool // skip provisioned AppX package removal
	SkipPackages    bool // skip servicing package removal by pattern
	SkipFileCleanup bool // skip aggressive manual file deletions
	SkipWinSxSWipe  bool // skip the WinSxS wipe-to-allowlist (the most aggressive, most likely-to-break-boot step)
	SkipRegTweaks   bool // skip registry tweaks + autounattend.xml install
	SkipServices    bool // skip service removal

	// WinRE selects what the file-cleanup stage does to winre.wim: WinREKeep
	// (leave the full recovery image in place) or WinREDonorStub (default
	// behavior when this is the zero value is WinREKeep; the CLI defaults to
	// WinREDonorStub explicitly -- see cmd/nano11-go). WinREDonorPath
	// overrides the donor winre.wim used for WinREDonorStub; left empty, it
	// is auto-sourced from the image's own winre.wim.
	WinRE          WinREMode
	WinREDonorPath string

	// KeepNICDrivers keeps the vendor network adapter driver families --
	// required for an image that must bring up networking on arbitrary
	// physical hardware.
	KeepNICDrivers bool
	// KeepDrivers keeps every DriverStore driver class the default pass
	// removes (printers, modems, biometric, etc.) on top of vendor NIC
	// families -- implies KeepNICDrivers, since "keep all drivers" would be
	// a lie otherwise. Callers building DebloatOptions directly (rather than
	// through the CLI) must set both fields themselves; Debloat does not
	// infer KeepNICDrivers from KeepDrivers.
	KeepDrivers bool
	// RemoveWebEngines also removes the essential web engine edgehtml.dll --
	// BREAKS OOBE (CloudExperienceHost renders through EdgeHTML), so off by
	// default; only for images that never run OOBE.
	RemoveWebEngines bool
	// KeepDefenderSearch keeps the Defender and Search servicing packages,
	// whose removal patterns were dead on 25H2 until they were revived and so
	// have never been covered by a passing install test.
	KeepDefenderSearch bool
	// RemoveStoreApps additionally removes the Microsoft Store and the
	// remaining Store-distributed UWP apps. The runtime frameworks they
	// depend on are kept unless RemoveUWPFrameworks is also set.
	RemoveStoreApps bool
	// RemoveUWPFrameworks additionally removes the UWP runtime frameworks
	// (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native). Implies
	// RemoveStoreApps -- removeBloatAppx (appx.go) removes the store-app
	// keyword set whenever either field is true, so callers need not set
	// RemoveStoreApps themselves for this to take effect.
	RemoveUWPFrameworks bool
	// RemoveAIFoundation removes the Windows AI Foundation / Copilot Runtime.
	// OOBE/shell-risk class (kept CoreAI may call into it).
	RemoveAIFoundation bool
	// RemoveIME clears all remaining input-method payload (CJK IME editors
	// and framework, plus SwiftKey typing prediction) for a Latin-only image.
	RemoveIME bool
	// RemoveAI removes the essential CoreAI SystemApp -- BREAKS OOBE (CoreAI
	// is OOBE-integrated in 25H2); off by default, only for images that never
	// run OOBE.
	RemoveAI bool

	// LZX selects the LZX encoder's speed/compression-ratio tradeoff for
	// every WIM this pass writes (see the gowim lzx package's preset
	// ladder). The zero value is lzx.Options{}'s own zero value, not a
	// usable preset -- callers should set this explicitly (e.g. lzx.Fast()).
	// Ignored entirely when NoCompression is set.
	LZX lzx.Options
	// NoCompression skips LZX entirely (wim.CompressionNone) for every WIM
	// this pass writes - both the final debloated output and the scratch
	// tmpExport intermediate. For a throwaway, single-boot Packer build
	// artifact there is nothing to gain from spending CPU time compressing
	// it and a lot to lose: LZX-recompressing a multi-GB install.wim was
	// confirmed on a live builder to take well over two hours of CPU time
	// even with the "fast"/"none" lzx_preset (that preset only controls
	// the LZX encoder's own effort level - the WIM still goes through real
	// LZX encoding either way), versus a few seconds uncompressed. Even
	// the "none" LZX preset still means real (if minimal-effort) LZX
	// encoding; this is the only way to skip compression outright.
	NoCompression bool

	// GrubPostInstallEFI, if non-empty, is a prebuilt GRUB x86_64-efi
	// standalone application (see grubpostinstall.go and cmd/nano11-go's
	// -grub-postinstall flag for the module list/config it must embed).
	// When set, Debloat stages it plus a SetupComplete.cmd/install-grub.ps1
	// pair into the image so that, on first boot after Setup completes,
	// GRUB -- not bootmgfw.efi directly -- becomes the firmware's default
	// boot target, chainloading Setup's own (renamed) bootmgfw.efi to
	// actually start Windows. Independent of SkipRegTweaks/autounattend.xml
	// installation: this is a live-Setup-run hook (SetupComplete.cmd), not a
	// registry/unattend tweak.
	GrubPostInstallEFI []byte
}

// Debloat runs the full install.wim debloat pass against the image at
// imageIndex (1-based) inside wimPath, writing the result to outPath.
// imageIndex == 0 lists the available images (by DisplayName) and returns
// without writing anything, mirroring the CLI's own "-image 0" listing mode;
// outPath is ignored in that case.
//
// This is nano11-go's central pipeline: export the target edition into a
// standalone WIM, apply each debloat stage in turn (AppX removal, servicing
// package removal, aggressive file cleanup, the WinSxS wipe, registry
// tweaks, service removal), reclaim space from everything deleted, and
// write the result.
func Debloat(wimPath, outPath string, imageIndex int, opts DebloatOptions) error {
	f, err := os.Open(wimPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", wimPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("read %s: %w", wimPath, err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		return err
	}
	xmlData, err := r.XMLData()
	if err != nil {
		return err
	}

	if imageIndex == 0 {
		fmt.Println("Available images:")
		for _, im := range xmlData.Images {
			fmt.Printf("  %d: %s (%s)\n", im.Index, im.DisplayName, im.Flags)
		}
		return nil
	}
	if outPath == "" {
		return fmt.Errorf("outPath is required when imageIndex is nonzero")
	}

	// Export just the target edition into a temporary, standalone WIM (only
	// the blobs that one edition actually references) via gowim's own
	// wim.ExportImage, mirroring DISM's /Export-Image -- this also slims the
	// several-edition source install.wim down to just the one edition we're
	// going to modify and reassemble.
	//
	// This step alone takes minutes (re-encoding several GB through the
	// pure-Go LZX encoder), so it is cached: a pre-existing, non-empty
	// tmpExport is reused as-is rather than redone, and is deliberately
	// left in place afterward (not cleaned up) so a subsequent run fixing
	// something downstream doesn't have to pay for it again. Delete it
	// by hand once you're done iterating.
	tmpExport := outPath + ".export.tmp"
	if fi, err := os.Stat(tmpExport); err == nil && fi.Size() > 0 {
		fmt.Printf("reusing existing export at %s\n", tmpExport)
	} else if err := exportImage(r, bt, xmlData, imageIndex, tmpExport, opts.LZX, opts.NoCompression); err != nil {
		return fmt.Errorf("export image %d: %w", imageIndex, err)
	}

	ef, err := os.Open(tmpExport)
	if err != nil {
		return err
	}
	defer ef.Close()
	efi, err := ef.Stat()
	if err != nil {
		return err
	}
	r2, err := wim.NewReader(ef, efi.Size())
	if err != nil {
		return err
	}
	bt2, err := r2.BlobTable()
	if err != nil {
		return err
	}
	xmlData2, err := r2.XMLData()
	if err != nil {
		return err
	}
	metaResources := bt2.MetadataResources()
	if len(metaResources) != 1 {
		return fmt.Errorf("expected exactly 1 image in the export, got %d", len(metaResources))
	}
	meta, err := r2.ImageMetadata(metaResources[0])
	if err != nil {
		return err
	}
	root := meta.Root

	if len(xmlData2.Images) != 1 {
		return fmt.Errorf("expected exactly 1 <IMAGE> in the export's XML data, got %d", len(xmlData2.Images))
	}
	winInfo := xmlData2.Images[0].Windows
	if winInfo == nil {
		return fmt.Errorf("exported image has no <WINDOWS> XML metadata")
	}
	languageCode := winInfo.DefaultLanguage
	if languageCode == "" {
		languageCode = "en-US"
		fmt.Println("warning: no default language found in image XML, assuming en-US")
	}
	arch := winInfo.ArchitectureName()
	fmt.Printf("Image language: %s, architecture: %s\n", languageCode, arch)

	newBlobs := map[wim.Hash][]byte{}

	hs, err := registry.LoadHiveSet(r2, root, bt2)
	if err != nil {
		return fmt.Errorf("load hive set: %w", err)
	}
	for _, name := range []string{registry.HiveSoftware, registry.HiveSystem, registry.HiveDefault, registry.HiveDefaultUser} {
		if hs.Hives[name] == nil {
			return fmt.Errorf("image is missing the %s hive", name)
		}
	}
	software := hs.Hives[registry.HiveSoftware]

	if opts.SkipAppx {
		fmt.Println("--- Skipping AppX removal (-skip-appx) ---")
	} else {
		fmt.Println("--- Removing provisioned AppX packages (bloatware) ---")
		if err := removeBloatAppx(r2, bt2, root, software, newBlobs, opts.RemoveStoreApps, opts.RemoveUWPFrameworks); err != nil {
			return fmt.Errorf("remove appx: %w", err)
		}
	}

	if opts.SkipPackages {
		fmt.Println("--- Skipping servicing package removal (-skip-packages) ---")
	} else {
		fmt.Println("--- Removing servicing packages (bloatware) ---")
		if err := removeBloatPackages(r2, bt2, root, languageCode, opts.KeepDefenderSearch); err != nil {
			return fmt.Errorf("remove packages: %w", err)
		}
	}

	if opts.SkipFileCleanup {
		fmt.Println("--- Skipping aggressive file cleanup (-skip-filecleanup) ---")
	} else {
		// The donor for WinRE == WinREDonorStub is the image's own winre.wim
		// (the stub grafts that same image's real Windows\Boot subtree back
		// in), so auto-source it here unless the caller passed an explicit
		// WinREDonorPath. Done here rather than inside file cleanup because
		// this is where the image reader (r2) is in scope.
		if opts.WinRE == WinREDonorStub && opts.WinREDonorPath == "" {
			donorPath, cleanupDonor, err := extractWinREDonorToTemp(r2, root, bt2)
			if err != nil {
				return fmt.Errorf("auto-source winre donor: %w", err)
			}
			defer cleanupDonor()
			if donorPath == "" {
				fmt.Println("No winre.wim present in image; skipping winre.wim stubbing")
				opts.WinRE = WinREKeep
			} else {
				opts.WinREDonorPath = donorPath
			}
		}
		fmt.Println("--- Performing aggressive manual file deletions ---")
		if err := runAggressiveFileCleanup(root, bt2, newBlobs, arch, opts); err != nil {
			return fmt.Errorf("file cleanup: %w", err)
		}
	}

	if opts.SkipWinSxSWipe {
		fmt.Println("--- Skipping WinSxS wipe-to-allowlist (-skip-winsxs-wipe) ---")
	} else {
		fmt.Println("--- Wiping WinSxS down to the survival allowlist ---")
		if err := wipeWinSxS(root, bt2, arch); err != nil {
			return fmt.Errorf("wipe winsxs: %w", err)
		}
	}

	if opts.SkipRegTweaks {
		fmt.Println("--- Skipping registry tweaks (-skip-regtweaks) ---")
	} else {
		fmt.Println("--- Applying registry tweaks ---")
		if err := applyRegistryTweaks(hs); err != nil {
			return fmt.Errorf("registry tweaks: %w", err)
		}
		if err := installAutounattend(root, bt2, newBlobs); err != nil {
			return fmt.Errorf("install autounattend.xml: %w", err)
		}
	}

	if len(opts.GrubPostInstallEFI) > 0 {
		if err := installGRUBPostInstall(root, bt2, newBlobs, opts.GrubPostInstallEFI); err != nil {
			return fmt.Errorf("install post-install GRUB bootloader: %w", err)
		}
	}

	if opts.SkipServices {
		fmt.Println("--- Skipping service removal (-skip-services) ---")
	} else {
		fmt.Println("--- Removing services ---")
		if err := removeBloatServices(hs.Hives[registry.HiveSystem].Hive.Root); err != nil {
			return fmt.Errorf("remove services: %w", err)
		}
	}

	fmt.Println("--- Saving registry hives ---")
	for name, h := range hs.Hives {
		nb, err := h.Save(bt2)
		if err != nil {
			return fmt.Errorf("save %s hive: %w", name, err)
		}
		if nb.Data != nil {
			newBlobs[nb.Hash] = nb.Data
		}
	}

	// Every removal stage above (AppX, servicing packages, file cleanup,
	// WinSxS wipe, services) only ever decremented a BlobDescriptor's
	// RefCount when unlinking a file from the tree -- none of them drop the
	// entry itself, so bt2 as it stands still lists every file we just
	// deleted, and WriteTo would faithfully write all of that dead payload
	// into the output WIM anyway. Rebuild the blob table from the final,
	// fully-edited tree so deleted content's bytes are actually reclaimed --
	// the same compaction real DISM /Export-Image (and nano11/tiny11's own
	// final Export-Image pass) performs for exactly this reason.
	fmt.Println("--- Reclaiming space from deleted files ---")
	// bt2 keeps every surviving blob's real Resource (offset/size in the
	// source r2), which the fallback BlobSource below still needs to
	// actually read blob content -- RebuildBlobTable's own output
	// deliberately zeroes Resource (real placement is only known once
	// WriteTo has written the destination file), so it's only used as the
	// table passed to WriteTo, not for reading source blobs.
	rebuiltBT, err := wim.RebuildBlobTable([]*wim.ImageMetadata{meta}, bt2)
	if err != nil {
		return fmt.Errorf("rebuild blob table: %w", err)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	compressionType := wim.CompressionType(wim.HdrFlagCompressLZX)
	if opts.NoCompression {
		compressionType = wim.CompressionNone
	}
	src := combinedBlobSource{overrides: newBlobs, fallback: wim.NewReaderBlobSource(r2, bt2)}
	_, err = wim.WriteTo(out, []*wim.ImageMetadata{meta}, rebuiltBT, xmlData2, src, wim.WriteOptions{
		CompressionType: compressionType,
		ChunkSize:       32768,
		BootIndex:       1,
		GUID:            randomGUID(),
		LZXOptions:      opts.LZX,
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("Wrote debloated single-image WIM to %s\n", outPath)
	return nil
}

func exportImage(r *wim.Reader, bt *wim.BlobTable, xmlData *wim.XMLData, index int, dest string, lzxOpts lzx.Options, noCompression bool) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	compressionType := wim.CompressionType(wim.HdrFlagCompressLZX)
	if noCompression {
		compressionType = wim.CompressionNone
	}
	_, err = wim.ExportImage(r, bt, xmlData, []int{index}, f, wim.WriteOptions{
		CompressionType: compressionType,
		ChunkSize:       32768,
		GUID:            randomGUID(),
		LZXOptions:      lzxOpts,
	})
	return err
}
