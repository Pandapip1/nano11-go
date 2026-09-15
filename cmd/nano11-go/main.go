// Command nano11-go is the thin CLI wrapper around the github.com/Pandapip1/
// nano11-go library package: it only parses flags/CLI args into the
// library's option structs (nano11go.DebloatOptions, nano11go.BootWimOptions,
// nano11go.ISOOptions) and calls into nano11go.Debloat/ShrinkBootWim/BuildISO.
// See the library package's own doc comment (debloat.go) for what the tool
// actually does end to end.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	nano11go "github.com/Pandapip1/nano11-go"

	"github.com/Pandapip1/gowim/lzx"
)

// lzxPresetFlag adapts the gowim lzx package's preset ladder to flag.Value
// so -lzx-preset can take a name instead of the individual tunables (which
// are deliberately not exposed here: the presets are measured points, ad-hoc
// combinations are not).
type lzxPresetFlag struct {
	opts *lzx.Options
	name *string
}

func (f lzxPresetFlag) String() string {
	if f.name == nil {
		return "fast"
	}
	return *f.name
}

func (f lzxPresetFlag) Set(s string) error {
	presets := map[string]func() lzx.Options{
		"fast":     lzx.Fast,
		"balanced": lzx.Balanced,
		"default":  lzx.DefaultOptions,
		"max":      lzx.Max,
		"none":     lzx.None,
	}
	p, ok := presets[s]
	if !ok {
		return fmt.Errorf("invalid -lzx-preset %q (want fast, balanced, default, max or none)", s)
	}
	*f.opts = p()
	*f.name = s
	return nil
}

// winREModeFlag adapts nano11go.WinREMode to flag.Value so -winre-mode can
// take a name (keep/delete/minimal-stub) instead of an opaque int.
type winREModeFlag struct{ mode *nano11go.WinREMode }

func (f winREModeFlag) String() string {
	if f.mode == nil {
		return "keep"
	}
	switch *f.mode {
	case nano11go.WinREDonorStub:
		return "donor-stub"
	default:
		return "keep"
	}
}

func (f winREModeFlag) Set(s string) error {
	switch s {
	case "keep":
		*f.mode = nano11go.WinREKeep
	case "donor-stub":
		*f.mode = nano11go.WinREDonorStub
	default:
		return fmt.Errorf("invalid -winre-mode %q (want keep or donor-stub; delete and minimal-stub were both tried and confirmed insufficient, see filecleanup.go)", s)
	}
	return nil
}

func main() {
	wimPath := flag.String("wim", "", "path to install.wim extracted from the ISO")
	outPath := flag.String("out", "", "path to write the debloated single-image install.wim")
	imageIndex := flag.Int("image", 0, "1-based image index to debloat (0 = list images and exit)")

	var opts nano11go.DebloatOptions
	// winre.wim (634 MB compressed) is the single largest item in the image
	// and the donor-stub cut for it (~606 MB) was validated by a real
	// Setup.exe install this session, so it is the default. The donor is
	// auto-sourced from the image's own winre.wim (see nano11go.Debloat);
	// -winre-donor is only needed to override it, and -winre-mode=keep opts
	// out entirely.
	opts.WinRE = nano11go.WinREDonorStub
	flag.BoolVar(&opts.SkipAppx, "skip-appx", false, "skip provisioned AppX package removal")
	flag.BoolVar(&opts.SkipPackages, "skip-packages", false, "skip servicing package removal by pattern")
	flag.BoolVar(&opts.SkipFileCleanup, "skip-filecleanup", false, "skip aggressive manual file deletions")
	flag.BoolVar(&opts.SkipWinSxSWipe, "skip-winsxs-wipe", false, "skip the WinSxS wipe-to-allowlist (the most aggressive, most likely-to-break-boot step)")
	flag.BoolVar(&opts.SkipRegTweaks, "skip-regtweaks", false, "skip registry tweaks + autounattend.xml install")
	flag.BoolVar(&opts.SkipServices, "skip-services", false, "skip service removal")
	flag.Var(winREModeFlag{&opts.WinRE}, "winre-mode", "what to do to winre.wim during file cleanup: donor-stub (default, ~606 MB saving, validated) or keep (leave the full recovery image in place)")
	flag.StringVar(&opts.WinREDonorPath, "winre-donor", "", "override the donor winre.wim for -winre-mode=donor-stub (default: auto-sourced from the image's own winre.wim, which needs no external file)")

	// boot.wim handling (bootwim.go/nano11go.ShrinkBootWim) is a separate
	// opt-in stage from the install.wim flow above, keyed on -boot-wim being
	// non-empty, since it operates on an entirely different file.
	var bootWim nano11go.BootWimOptions
	bootWimPath := flag.String("boot-wim", "", "path to boot.wim extracted from the ISO; if set, shrinks it to just the setup image (index 2), discarding the WinPE boot image")
	bootWimOutPath := flag.String("boot-wim-out", "", "path to write the shrunk boot.wim (required if -boot-wim is set)")
	flag.BoolVar(&bootWim.SkipRegTweaks, "skip-boot-regtweaks", false, "skip the requirement-bypass/BitLocker registry tweaks applied to boot.wim's setup image")
	flag.BoolVar(&bootWim.SkipLocaleTrim, "skip-boot-locale-trim", false, "skip removing non-en-US locale directories and their owning WinSxS packages from boot.wim's setup image")
	flag.BoolVar(&bootWim.SkipFileCleanup, "skip-boot-filecleanup", false, "skip the font/speech/enterprise-storage-driver trim applied to boot.wim's setup image")

	// The three groups below are the 2026-08-19 additions most likely to
	// break a real install or first boot, each isolated behind its own flag
	// so a failure can be bisected by flipping flags rather than by editing
	// and rebuilding. They are also genuinely useful on their own: an image
	// destined for physical hardware wants -keep-nic-drivers, for instance.
	flag.BoolVar(&opts.KeepNICDrivers, "keep-nic-drivers", false, "keep the 67 vendor network adapter driver families (242 MB) -- required for an image that must bring up networking on arbitrary physical hardware")
	// keepDrivers keeps every DriverStore driver class the default pass
	// removes (printers, modems, biometric, plus boot.wim's enterprise
	// storage/HBA set) on top of the vendor NIC families -- implies
	// keepNICDrivers, since "keep all drivers" would be a lie otherwise.
	flag.BoolVar(&opts.KeepDrivers, "keep-drivers", false, "keep every DriverStore driver class removed by default (printers, modems, biometric, etc.) and boot.wim's enterprise storage/HBA drivers, on top of vendor NIC families -- implies -keep-nic-drivers; for an image that must support arbitrary physical hardware")
	flag.BoolVar(&opts.RemoveWebEngines, "remove-web-engines", false, "also remove the essential web engine edgehtml.dll (~46 MB) -- BREAKS OOBE (CloudExperienceHost renders through EdgeHTML), so off by default; mshtml.dll is already removed by default as non-essential; only for images that never run OOBE")
	flag.BoolVar(&opts.KeepDefenderSearch, "keep-defender-search", false, "keep the Defender and Search servicing packages, whose removal patterns were dead on 25H2 until they were revived and so have never been covered by a passing install test")
	// removeAI removes the ESSENTIAL AI component, the CoreAI SystemApp, on
	// top of the non-essential AI resources/data that file cleanup removes by
	// default. It is OFF by default and opt-in, because removing CoreAI BREAKS
	// OOBE: a 2026-08-20 QEMU bisect showed its absence drops OOBE to the "Why
	// did my PC restart?" recovery screen -- CoreAI is an OOBE-integrated
	// SystemApp in 25H2, exactly like edgehtml. The non-essential AI bits
	// (DirectML MUI resources ~21 MB, WU ML models ~1 MB) are OOBE-safe and
	// always removed; only CoreAI's ~19 MB is gated here.
	flag.BoolVar(&opts.RemoveAI, "remove-ai", false, "also remove the essential CoreAI SystemApp (~19 MB) -- BREAKS OOBE (CoreAI is OOBE-integrated in 25H2, bisected 2026-08-20), so off by default; the DirectML resources + WU ML models are already removed by default as non-essential; only for images that never run OOBE")
	// removeStoreApps additionally removes the Microsoft Store and the
	// remaining Store-distributed UWP apps (Calculator, Terminal, App
	// Installer/winget, Phone Link CrossDevice, MPEG2/WebMedia extensions) --
	// see storeAppxKeywords. Opt-in, off by default: these are apps a user may
	// want, unlike the telemetry/consumer bloat removed by default. The
	// runtime frameworks they depend on are kept.
	flag.BoolVar(&opts.RemoveStoreApps, "remove-store-apps", false, "also remove the Microsoft Store and the remaining Store-distributed UWP apps that survive the default pass (Calculator, Terminal, App Installer/winget, Phone Link CrossDevice, MPEG2/WebMedia extensions), ~83 MB; the UWP runtime frameworks they depend on are kept")
	// removeUWPFrameworks additionally removes the UWP runtime frameworks
	// (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native). Implies
	// removeStoreApps (removing frameworks while apps still need them would
	// break those apps). Higher risk than removeStoreApps: the desktop shell
	// may link these, so it needs a boot-to-desktop test, not just OOBE.
	flag.BoolVar(&opts.RemoveUWPFrameworks, "remove-uwp-frameworks", false, "also remove the UWP runtime frameworks (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native), ~58 MB; implies -remove-store-apps. Higher risk -- the desktop shell may depend on these")
	flag.BoolVar(&opts.RemoveAIFoundation, "remove-ai-foundation", false, "remove the Windows AI Foundation / Copilot Runtime: the vNext app-runtime SystemApp (on-device generative AI, ~40 MB), AugLoop, and the System32 ML inference DLLs (directml, onnxruntime, WinML, SmartActionPlatform), ~47 MB. OOBE/shell-risk class -- CoreAI is kept and may call into it")
	flag.BoolVar(&opts.RemoveIME, "remove-ime", false, "remove all remaining input-method payload for a Latin-only image (~21 MB): the CJK IME editors (Windows\\IME, SysWOW64\\IME), the IME broker/API (System32\\IME\\SHARED) and InputMethod shared code, and the SwiftKey typing-prediction models (Windows\\SKB). Basic keyboard input is unaffected; CJK input and typing suggestions are lost")

	// ISO authoring (isoimage.go/nano11go.BuildISO). Like -boot-wim, this is
	// an opt-in stage keyed on one flag being non-empty, and it runs last: it
	// consumes what the install.wim and boot.wim stages above produced.
	var isoOpts nano11go.ISOOptions
	flag.StringVar(&isoOpts.Dir, "iso-dir", "", "path to an extracted Windows ISO tree; if set, authors a bootable ISO from it after the stages above (the tree is prepared in place: install.wim/boot.wim placed, root trimmed to nano11's keepList)")
	flag.StringVar(&isoOpts.Out, "iso-out", "", "path to write the authored bootable ISO (required if -iso-dir is set)")
	flag.StringVar(&isoOpts.VolID, "iso-volid", "Nano11Go", "volume identifier (disc label) of the authored ISO")
	flag.StringVar(&isoOpts.InstallWim, "iso-install-wim", "", "install.wim to place at sources/install.wim in -iso-dir (default: whatever -out wrote; empty and no -out leaves the tree's existing copy alone)")
	flag.StringVar(&isoOpts.BootWim, "iso-boot-wim", "", "boot.wim to place at sources/boot.wim in -iso-dir (default: whatever -boot-wim-out wrote; empty and no -boot-wim-out leaves the tree's existing copy alone)")
	flag.BoolVar(&isoOpts.SkipAutounattend, "skip-iso-autounattend", false, "do not place nano11's embedded autounattend.xml at the authored ISO's root")
	flag.BoolVar(&isoOpts.KeepExtras, "keep-iso-extras", false, "keep the low-risk media extras that are removed by default: the offline .NET 3.5 cab (~71 MB), Setup credits text (~18 MB), and CJK boot fonts (~27 MB) -- none of which affect install, boot, or OOBE")
	flag.StringVar(&isoOpts.GrubEFI, "grub-efi", "", "path to an a1ive GRUB EFI application (see contrib/grub/); if set, the authored ISO's UEFI boot entry loads GRUB instead of Windows' boot manager, giving a boot menu that autoprobes installed OSes and boots Windows Setup via wimboot. Optical-bootable and pure UEFI. Requires Secure Boot off; the GRUB binary is not vendored -- build it with contrib/grub/build-grub.sh")
	flag.BoolVar(&isoOpts.SkipHybridMBR, "skip-iso-hybrid-mbr", false, "do not stamp an isohybrid MBR into the authored ISO; by default one is added so the image is also UEFI-bootable when written byte-for-byte to a USB stick (dd, GNOME Disks, Rufus DD-image mode), not only from real/virtual optical media")
	flag.StringVar(&isoOpts.GrubBIOSHybrid, "grub-bios-hybrid", "", "path to a1ive GRUB's boot_hybrid.img (see contrib/grub/build-grub.sh); together with -grub-bios-eltorito, replaces the BIOS boot entry with GRUB (Windows' own etfsboot.com cannot run outside an El Torito CD-emulation context, so it can never be USB-bootable in BIOS mode) and patches the isohybrid MBR so that entry is also reachable from a raw-written USB stick, not just optical/VM BIOS boot")
	flag.StringVar(&isoOpts.GrubBIOSEltorito, "grub-bios-eltorito", "", "path to the GRUB BIOS El Torito image to pair with -grub-bios-hybrid: contrib/grub/BIOS_ELTORITO_MENU.img (the same autoprobe/wimboot/memtest/fwsetup menu as -grub-efi, now BIOS-reachable too) when also using -grub-efi, or BIOS_ELTORITO_SILENT.img otherwise (silently wimboots straight to Setup, matching today's Windows-direct-boot behavior but now from a raw USB stick in BIOS mode too)")

	// Distinct from -grub-efi/-grub-bios-*: those put GRUB on the install
	// MEDIA as a front-end that boots Setup (needs a1ive's fork, for
	// wimboot's optical-media boot.wim access). This instead makes GRUB the
	// INSTALLED SYSTEM's own persistent bootloader after Setup finishes --
	// stock GRUB is enough for this (chainloading an already-installed,
	// already-on-a-real-FAT32-ESP bootmgfw.efi needs no wimboot at all), and
	// it is baked into install.wim itself (grubpostinstall.go), not the ISO
	// media, since it must survive after the install disc is gone. See
	// grubpostinstall.go for the SetupComplete.cmd/bcdedit mechanism. Build
	// with e.g.:
	//   grub-mkstandalone -O x86_64-efi \
	//     --modules="part_gpt part_msdos fat search search_fs_file chain" \
	//     -o grubx64.efi "boot/grub/grub.cfg=<cfg unconditionally running: \
	//     search --no-floppy --file --set=root /EFI/Microsoft/Boot/bootmgfw-orig.efi; \
	//     chainloader /EFI/Microsoft/Boot/bootmgfw-orig.efi; boot>"
	grubPostInstallPath := flag.String("grub-postinstall", "", "path to a prebuilt GRUB x86_64-efi standalone application (stock GRUB is sufficient; see this flag's doc comment in main.go for the exact grub-mkstandalone invocation and embedded grub.cfg); if set, stages it plus a SetupComplete.cmd + install-grub.ps1 into install.wim so GRUB becomes the ESP's default boot target after Setup completes, chainloading Windows' own (renamed) bootmgfw.efi")

	// Default "fast", not gowim's own ratio-first default: this tool's
	// workload is re-encoding multi-gigabyte images end to end (the
	// install.wim export pass alone re-encodes every blob of a ~7.4 GB
	// image, and the final write does it again), where the measured
	// tradeoff is lopsided. Measured 2026-08-18 on a 24-core x86-64
	// machine over 29.4 MiB of real Windows install-image data compressed
	// in 32 KiB chunks across all cores (gowim lzx.Options's own doc has
	// the corpus and full ladder): fast 20.5 MB/s, balanced 3.2 MB/s
	// (+0.66% size vs default), default 0.63 MB/s, max 0.21 MB/s (-0.07%).
	// Those are raw encoder rates on already-in-memory chunks. Projecting
	// them straight onto the image size understates a real run by ~3x,
	// because a run also decompresses the source, re-encodes twice (export
	// pass plus final write), and does the non-compression debloat work in
	// between. The projections below are therefore anchored on a real
	// measured end-to-end run instead: a full `-image 6` debloat of the
	// 7.4 GB install.wim took 1761 s (29.4 min) wall-clock on 2026-08-18 at
	// what was then the fast rung (13.8 MB/s), scaled here by each rung's
	// measured rate. That anchoring also matches the observed behavior of
	// an aborted default-preset run, which was tracking to ~13 h rather
	// than the ~3.5 h raw arithmetic predicts.
	lzxPresetName := "fast"
	opts.LZX = lzx.Fast()
	flag.Var(lzxPresetFlag{&opts.LZX, &lzxPresetName}, "lzx-preset",
		"LZX encoder speed/size tradeoff for every WIM written: fast (default), balanced, default, max or none. "+
			"Measured 2026-08-18 (24-core, 29.4 MiB of real Windows image data, 32 KiB chunks, all cores): fast 20.5 MB/s at +1.75% output size, "+
			"balanced 3.2 MB/s at +0.66%, default 0.63 MB/s, max 0.21 MB/s at -0.07%. "+
			"Scaled from a real measured 1761 s (29.4 min) end-to-end debloat of the 7.4 GB install.wim, a full run is roughly 20 min (fast) vs ~2 h (balanced) vs ~11 h (default) vs ~32 h (max); "+
			"pick balanced or default when output size matters more than turnaround. "+
			"none skips LZX entirely (every chunk written as a raw UNCOMPRESSED block): fastest possible run but a much larger WIM -- only for iterating on debloat logic itself, never for an image meant to ship")
	flag.Parse()
	fmt.Printf("LZX preset: %s\n", lzxPresetName)
	bootWim.LZX = opts.LZX

	if opts.KeepDrivers {
		opts.KeepNICDrivers = true
	}
	bootWim.KeepDrivers = opts.KeepDrivers

	if *bootWimPath != "" {
		if *bootWimOutPath == "" {
			log.Fatal("-boot-wim-out is required when -boot-wim is given")
		}
		fmt.Println("--- Shrinking boot.wim to just the setup image ---")
		if err := nano11go.ShrinkBootWim(*bootWimPath, *bootWimOutPath, bootWim); err != nil {
			log.Fatalf("shrink boot.wim: %v", err)
		}
		fmt.Printf("Wrote shrunk boot.wim to %s\n", *bootWimOutPath)
	}

	// -wim is required only when no other stage was asked for, so that
	// -boot-wim and -iso-dir can each be run on their own (re-authoring an
	// ISO around an install.wim debloated by an earlier run is the common
	// case: the debloat costs tens of minutes, the ISO a few seconds).
	if *wimPath == "" && *bootWimPath == "" && isoOpts.Dir == "" {
		log.Fatal("-wim is required")
	}

	if *grubPostInstallPath != "" {
		data, err := os.ReadFile(*grubPostInstallPath)
		if err != nil {
			log.Fatalf("-grub-postinstall: %v", err)
		}
		opts.GrubPostInstallEFI = data
	}

	if *wimPath != "" {
		if err := nano11go.Debloat(*wimPath, *outPath, *imageIndex, opts); err != nil {
			log.Fatal(err)
		}
	}

	if isoOpts.Dir != "" {
		// Default each ISO input to what this same run just produced, so a
		// full pipeline needs no repeated paths; an explicit flag overrides.
		if isoOpts.InstallWim == "" && *wimPath != "" && *imageIndex != 0 {
			isoOpts.InstallWim = *outPath
		}
		if isoOpts.BootWim == "" {
			isoOpts.BootWim = *bootWimOutPath
		}
		fmt.Println("--- Authoring bootable ISO ---")
		if err := nano11go.BuildISO(isoOpts); err != nil {
			log.Fatalf("author ISO: %v", err)
		}
	}
}
