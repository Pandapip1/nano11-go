// nano11-go is a full, out-of-tree port of nano11builder.ps1
// (github.com/ntdevlabs/nano11) onto the gowim libraries
// (github.com/Pandapip1/gowim/*): it reproduces nano11's entire debloat
// pass against a real Windows install.wim edition -- AppX removal,
// servicing-package removal, the aggressive WinSxS wipe-to-allowlist,
// misc file deletions, and the full registry tweak set, including the
// steps nano11's own comments flag as breakage-prone -- using only
// gowim's Go packages: no DISM, no mounted image, no admin rights.
//
// It also authors the final bootable ISO itself, over gowim's iso package
// (ECMA-119 + the UDF 1.02 bridge + El Torito) -- see isoimage.go and the
// -iso-dir/-iso-out flags. That step used to be a shell script calling
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
// WinSxS trim) IS ported/implemented -- see bootwim.go -- but is a
// separate opt-in step (-boot-wim/-boot-wim-out) from the install.wim flow
// below, since it operates on an entirely different file. None of these
// steps reduce size when measured against the untouched source boot.wim
// (see TODO.md) -- but a registry-tweak pass forces a full re-encode of
// boot.wim regardless, and measured against that real alternative (a
// re-encode that keeps everything), both the image-drop and the locale/
// WinSxS trim are genuine, verified size wins. See TODO.md for the exact
// numbers and the QEMU/OVMF boot verification this was based on.
//
// Every WIM this tool writes is LZX-compressed through gowim's pure-Go
// encoder, and that encoder's speed/ratio tradeoff is selectable with
// -lzx-preset (default "fast"). It is worth understanding before starting a
// run: a full pass re-encodes the whole image twice (the export pass, then
// the final write), so the preset, not the debloat work, is what sets the
// wall time. See the flag's own help text for the measured ladder.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// stageFlags lets each risky debloat stage be independently disabled from
// the command line, so a boot-failure regression can be bisected (which
// stage actually causes it) without editing code and rebuilding between
// attempts -- important given each full run costs tens of minutes.
type stageFlags struct {
	skipAppx            bool
	skipPackages        bool
	skipFileCleanup     bool
	skipWinSxSWipe      bool
	skipRegTweaks       bool
	skipServices        bool
	winRE               winREMode
	winREDonorPath      string
	bootWimPath         string
	bootWimOutPath      string
	skipBootRegTweaks   bool
	skipBootLocaleTrim  bool
	skipBootFileCleanup bool
	// The three groups below are the 2026-08-19 additions most likely to
	// break a real install or first boot, each isolated behind its own flag
	// so a failure can be bisected by flipping flags rather than by editing
	// and rebuilding. They are also genuinely useful on their own: an image
	// destined for physical hardware wants keepNICDrivers, for instance.
	keepNICDrivers     bool
	removeWebEngines   bool
	keepDefenderSearch bool
	// removeStoreApps additionally removes the Microsoft Store and the
	// remaining Store-distributed UWP apps (Calculator, Terminal, App
	// Installer/winget, Phone Link CrossDevice, MPEG2/WebMedia extensions) --
	// see storeAppxKeywords. Opt-in, off by default: these are apps a user may
	// want, unlike the telemetry/consumer bloat removed by default. The
	// runtime frameworks they depend on are kept.
	removeStoreApps bool
	// removeAIFoundation removes the Windows AI Foundation / Copilot Runtime
	// (the vNext app-runtime SystemApp hosting the on-device generative-AI
	// stack, AugLoop, and the System32 ML inference DLLs). Opt-in; OOBE/shell-
	// risk class (kept CoreAI may call into it), so it needs a boot test.
	removeAIFoundation bool
	// removeIME clears all remaining input-method payload (CJK IME editors and
	// framework, plus SwiftKey typing prediction) for a Latin-only image.
	// Opt-in: basic keyboard input is unaffected, but it removes the IME
	// broker/API left as insurance by default, so it warrants a boot test.
	removeIME bool
	// removeUWPFrameworks additionally removes the UWP runtime frameworks
	// (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native). Implies
	// removeStoreApps (removing frameworks while apps still need them would
	// break those apps). Higher risk than removeStoreApps: the desktop shell
	// may link these, so it needs a boot-to-desktop test, not just OOBE.
	removeUWPFrameworks bool
	// removeAI removes the ESSENTIAL AI component, the CoreAI SystemApp, on
	// top of the non-essential AI resources/data that file cleanup removes by
	// default. It is OFF by default and opt-in, because removing CoreAI BREAKS
	// OOBE: a 2026-08-20 QEMU bisect showed its absence drops OOBE to the "Why
	// did my PC restart?" recovery screen -- CoreAI is an OOBE-integrated
	// SystemApp in 25H2, exactly like edgehtml. The non-essential AI bits
	// (DirectML MUI resources ~21 MB, WU ML models ~1 MB) are OOBE-safe and
	// always removed; only CoreAI's ~19 MB is gated here. See filecleanup.go's
	// essential/non-essential split. -remove-ai opts in for never-OOBE images.
	removeAI bool
	// lzx selects the LZX encoder's speed/compression-ratio tradeoff for
	// every WIM this tool writes (see lzxPresetFlag). It is a stage flag
	// rather than a constant because it is the single largest determinant
	// of a run's wall time.
	lzx lzx.Options
}

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
	}
	p, ok := presets[s]
	if !ok {
		return fmt.Errorf("invalid -lzx-preset %q (want fast, balanced, default or max)", s)
	}
	*f.opts = p()
	*f.name = s
	return nil
}

// winREModeFlag adapts winREMode to flag.Value so -winre-mode can take a
// name (keep/delete/minimal-stub) instead of an opaque int.
type winREModeFlag struct{ mode *winREMode }

func (f winREModeFlag) String() string {
	if f.mode == nil {
		return "keep"
	}
	switch *f.mode {
	case winREDonorStub:
		return "donor-stub"
	default:
		return "keep"
	}
}

func (f winREModeFlag) Set(s string) error {
	switch s {
	case "keep":
		*f.mode = winREKeep
	case "donor-stub":
		*f.mode = winREDonorStub
	default:
		return fmt.Errorf("invalid -winre-mode %q (want keep or donor-stub; delete and minimal-stub were both tried and confirmed insufficient, see filecleanup.go)", s)
	}
	return nil
}

func main() {
	wimPath := flag.String("wim", "", "path to install.wim extracted from the ISO")
	outPath := flag.String("out", "", "path to write the debloated single-image install.wim")
	imageIndex := flag.Int("image", 0, "1-based image index to debloat (0 = list images and exit)")
	var stages stageFlags
	// winre.wim (634 MB compressed) is the single largest item in the image
	// and the donor-stub cut for it (~606 MB) was validated by a real
	// Setup.exe install this session, so it is the default. The donor is
	// auto-sourced from the image's own winre.wim (see run()); -winre-donor
	// is only needed to override it, and -winre-mode=keep opts out entirely.
	stages.winRE = winREDonorStub
	flag.BoolVar(&stages.skipAppx, "skip-appx", false, "skip provisioned AppX package removal")
	flag.BoolVar(&stages.skipPackages, "skip-packages", false, "skip servicing package removal by pattern")
	flag.BoolVar(&stages.skipFileCleanup, "skip-filecleanup", false, "skip aggressive manual file deletions")
	flag.BoolVar(&stages.skipWinSxSWipe, "skip-winsxs-wipe", false, "skip the WinSxS wipe-to-allowlist (the most aggressive, most likely-to-break-boot step)")
	flag.BoolVar(&stages.skipRegTweaks, "skip-regtweaks", false, "skip registry tweaks + autounattend.xml install")
	flag.BoolVar(&stages.skipServices, "skip-services", false, "skip service removal")
	flag.Var(winREModeFlag{&stages.winRE}, "winre-mode", "what to do to winre.wim during file cleanup: donor-stub (default, ~606 MB saving, validated) or keep (leave the full recovery image in place)")
	flag.StringVar(&stages.winREDonorPath, "winre-donor", "", "override the donor winre.wim for -winre-mode=donor-stub (default: auto-sourced from the image's own winre.wim, which needs no external file)")
	flag.StringVar(&stages.bootWimPath, "boot-wim", "", "path to boot.wim extracted from the ISO; if set, shrinks it to just the setup image (index 2), discarding the WinPE boot image")
	flag.StringVar(&stages.bootWimOutPath, "boot-wim-out", "", "path to write the shrunk boot.wim (required if -boot-wim is set)")
	flag.BoolVar(&stages.skipBootRegTweaks, "skip-boot-regtweaks", false, "skip the requirement-bypass/BitLocker registry tweaks applied to boot.wim's setup image")
	flag.BoolVar(&stages.skipBootLocaleTrim, "skip-boot-locale-trim", false, "skip removing non-en-US locale directories and their owning WinSxS packages from boot.wim's setup image")
	flag.BoolVar(&stages.skipBootFileCleanup, "skip-boot-filecleanup", false, "skip the font/speech/enterprise-storage-driver trim applied to boot.wim's setup image")
	flag.BoolVar(&stages.keepNICDrivers, "keep-nic-drivers", false, "keep the 67 vendor network adapter driver families (242 MB) -- required for an image that must bring up networking on arbitrary physical hardware")
	flag.BoolVar(&stages.removeWebEngines, "remove-web-engines", false, "also remove the essential web engine edgehtml.dll (~46 MB) -- BREAKS OOBE (CloudExperienceHost renders through EdgeHTML), so off by default; mshtml.dll is already removed by default as non-essential; only for images that never run OOBE")
	flag.BoolVar(&stages.keepDefenderSearch, "keep-defender-search", false, "keep the Defender and Search servicing packages, whose removal patterns were dead on 25H2 until they were revived and so have never been covered by a passing install test")
	flag.BoolVar(&stages.removeAI, "remove-ai", false, "also remove the essential CoreAI SystemApp (~19 MB) -- BREAKS OOBE (CoreAI is OOBE-integrated in 25H2, bisected 2026-08-20), so off by default; the DirectML resources + WU ML models are already removed by default as non-essential; only for images that never run OOBE")
	flag.BoolVar(&stages.removeStoreApps, "remove-store-apps", false, "also remove the Microsoft Store and the remaining Store-distributed UWP apps that survive the default pass (Calculator, Terminal, App Installer/winget, Phone Link CrossDevice, MPEG2/WebMedia extensions), ~83 MB; the UWP runtime frameworks they depend on are kept")
	flag.BoolVar(&stages.removeUWPFrameworks, "remove-uwp-frameworks", false, "also remove the UWP runtime frameworks (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native), ~58 MB; implies -remove-store-apps. Higher risk -- the desktop shell may depend on these")
	flag.BoolVar(&stages.removeAIFoundation, "remove-ai-foundation", false, "remove the Windows AI Foundation / Copilot Runtime: the vNext app-runtime SystemApp (on-device generative AI, ~40 MB), AugLoop, and the System32 ML inference DLLs (directml, onnxruntime, WinML, SmartActionPlatform), ~47 MB. OOBE/shell-risk class -- CoreAI is kept and may call into it")
	flag.BoolVar(&stages.removeIME, "remove-ime", false, "remove all remaining input-method payload for a Latin-only image (~21 MB): the CJK IME editors (Windows\\IME, SysWOW64\\IME), the IME broker/API (System32\\IME\\SHARED) and InputMethod shared code, and the SwiftKey typing-prediction models (Windows\\SKB). Basic keyboard input is unaffected; CJK input and typing suggestions are lost")
	// ISO authoring (isoimage.go). Like -boot-wim, this is an opt-in stage
	// keyed on one flag being non-empty, and it runs last: it consumes what
	// the install.wim and boot.wim stages above produced.
	var isoOpts isoFlags
	flag.StringVar(&isoOpts.dir, "iso-dir", "", "path to an extracted Windows ISO tree; if set, authors a bootable ISO from it after the stages above (the tree is prepared in place: install.wim/boot.wim placed, root trimmed to nano11's keepList)")
	flag.StringVar(&isoOpts.out, "iso-out", "", "path to write the authored bootable ISO (required if -iso-dir is set)")
	flag.StringVar(&isoOpts.volID, "iso-volid", "Nano11Go", "volume identifier (disc label) of the authored ISO")
	flag.StringVar(&isoOpts.installWim, "iso-install-wim", "", "install.wim to place at sources/install.wim in -iso-dir (default: whatever -out wrote; empty and no -out leaves the tree's existing copy alone)")
	flag.StringVar(&isoOpts.bootWim, "iso-boot-wim", "", "boot.wim to place at sources/boot.wim in -iso-dir (default: whatever -boot-wim-out wrote; empty and no -boot-wim-out leaves the tree's existing copy alone)")
	flag.BoolVar(&isoOpts.skipAutounattend, "skip-iso-autounattend", false, "do not place nano11's embedded autounattend.xml at the authored ISO's root")
	flag.BoolVar(&isoOpts.keepExtras, "keep-iso-extras", false, "keep the low-risk media extras that are removed by default: the offline .NET 3.5 cab (~71 MB), Setup credits text (~18 MB), and CJK boot fonts (~27 MB) -- none of which affect install, boot, or OOBE")
	flag.StringVar(&isoOpts.grubEFI, "grub-efi", "", "path to an a1ive GRUB EFI application (see contrib/grub/); if set, the authored ISO's UEFI boot entry loads GRUB instead of Windows' boot manager, giving a boot menu that autoprobes installed OSes and boots Windows Setup via wimboot. Optical-bootable and pure UEFI. Requires Secure Boot off; the GRUB binary is not vendored -- build it with contrib/grub/build-grub.sh")
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
	stages.lzx = lzx.Fast()
	flag.Var(lzxPresetFlag{&stages.lzx, &lzxPresetName}, "lzx-preset",
		"LZX encoder speed/size tradeoff for every WIM written: fast (default), balanced, default or max. "+
			"Measured 2026-08-18 (24-core, 29.4 MiB of real Windows image data, 32 KiB chunks, all cores): fast 20.5 MB/s at +1.75% output size, "+
			"balanced 3.2 MB/s at +0.66%, default 0.63 MB/s, max 0.21 MB/s at -0.07%. "+
			"Scaled from a real measured 1761 s (29.4 min) end-to-end debloat of the 7.4 GB install.wim, a full run is roughly 20 min (fast) vs ~2 h (balanced) vs ~11 h (default) vs ~32 h (max); "+
			"pick balanced or default when output size matters more than turnaround")
	flag.Parse()
	fmt.Printf("LZX preset: %s\n", lzxPresetName)

	if stages.bootWimPath != "" {
		if stages.bootWimOutPath == "" {
			log.Fatal("-boot-wim-out is required when -boot-wim is given")
		}
		fmt.Println("--- Shrinking boot.wim to just the setup image ---")
		if err := shrinkBootWim(stages.bootWimPath, stages.bootWimOutPath, stages.skipBootRegTweaks, stages.skipBootLocaleTrim, stages.skipBootFileCleanup, stages.lzx); err != nil {
			log.Fatalf("shrink boot.wim: %v", err)
		}
		fmt.Printf("Wrote shrunk boot.wim to %s\n", stages.bootWimOutPath)
	}

	// -wim is required only when no other stage was asked for, so that
	// -boot-wim and -iso-dir can each be run on their own (re-authoring an
	// ISO around an install.wim debloated by an earlier run is the common
	// case: the debloat costs tens of minutes, the ISO a few seconds).
	if *wimPath == "" && stages.bootWimPath == "" && isoOpts.dir == "" {
		log.Fatal("-wim is required")
	}

	if *wimPath != "" {
		if err := run(*wimPath, *outPath, *imageIndex, stages); err != nil {
			log.Fatal(err)
		}
	}

	if isoOpts.dir != "" {
		// Default each ISO input to what this same run just produced, so a
		// full pipeline needs no repeated paths; an explicit flag overrides.
		if isoOpts.installWim == "" && *wimPath != "" && *imageIndex != 0 {
			isoOpts.installWim = *outPath
		}
		if isoOpts.bootWim == "" {
			isoOpts.bootWim = stages.bootWimOutPath
		}
		fmt.Println("--- Authoring bootable ISO ---")
		if err := buildISO(isoOpts); err != nil {
			log.Fatalf("author ISO: %v", err)
		}
	}
}

func run(wimPath, outPath string, imageIndex int, stages stageFlags) error {
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
		return fmt.Errorf("-out is required when -image is given")
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
	} else if err := exportImage(r, bt, xmlData, imageIndex, tmpExport, stages.lzx); err != nil {
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

	if stages.skipAppx {
		fmt.Println("--- Skipping AppX removal (-skip-appx) ---")
	} else {
		fmt.Println("--- Removing provisioned AppX packages (bloatware) ---")
		if err := removeBloatAppx(r2, bt2, root, software, newBlobs, stages.removeStoreApps, stages.removeUWPFrameworks); err != nil {
			return fmt.Errorf("remove appx: %w", err)
		}
	}

	if stages.skipPackages {
		fmt.Println("--- Skipping servicing package removal (-skip-packages) ---")
	} else {
		fmt.Println("--- Removing servicing packages (bloatware) ---")
		if err := removeBloatPackages(r2, bt2, root, languageCode, stages.keepDefenderSearch); err != nil {
			return fmt.Errorf("remove packages: %w", err)
		}
	}

	if stages.skipFileCleanup {
		fmt.Println("--- Skipping aggressive file cleanup (-skip-filecleanup) ---")
	} else {
		// The donor for -winre-mode=donor-stub is the image's own winre.wim
		// (the stub grafts that same image's real Windows\Boot subtree back
		// in), so auto-source it here unless the user passed an explicit
		// -winre-donor. Done in run() rather than inside file cleanup because
		// this is where the image reader (r2) is in scope.
		if stages.winRE == winREDonorStub && stages.winREDonorPath == "" {
			donorPath, cleanupDonor, err := extractWinREDonorToTemp(r2, root, bt2)
			if err != nil {
				return fmt.Errorf("auto-source winre donor: %w", err)
			}
			defer cleanupDonor()
			if donorPath == "" {
				fmt.Println("No winre.wim present in image; skipping winre.wim stubbing")
				stages.winRE = winREKeep
			} else {
				stages.winREDonorPath = donorPath
			}
		}
		fmt.Println("--- Performing aggressive manual file deletions ---")
		if err := runAggressiveFileCleanup(root, bt2, newBlobs, arch, stages); err != nil {
			return fmt.Errorf("file cleanup: %w", err)
		}
	}

	if stages.skipWinSxSWipe {
		fmt.Println("--- Skipping WinSxS wipe-to-allowlist (-skip-winsxs-wipe) ---")
	} else {
		fmt.Println("--- Wiping WinSxS down to the survival allowlist ---")
		if err := wipeWinSxS(root, bt2, arch); err != nil {
			return fmt.Errorf("wipe winsxs: %w", err)
		}
	}

	if stages.skipRegTweaks {
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

	if stages.skipServices {
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

	src := combinedBlobSource{overrides: newBlobs, fallback: wim.NewReaderBlobSource(r2, bt2)}
	_, err = wim.WriteTo(out, []*wim.ImageMetadata{meta}, rebuiltBT, xmlData2, src, wim.WriteOptions{
		CompressionType: wim.HdrFlagCompressLZX,
		ChunkSize:       32768,
		BootIndex:       1,
		GUID:            randomGUID(),
		LZXOptions:      stages.lzx,
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("Wrote debloated single-image WIM to %s\n", outPath)
	return nil
}

func exportImage(r *wim.Reader, bt *wim.BlobTable, xmlData *wim.XMLData, index int, dest string, lzxOpts lzx.Options) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = wim.ExportImage(r, bt, xmlData, []int{index}, f, wim.WriteOptions{
		CompressionType: wim.HdrFlagCompressLZX,
		ChunkSize:       32768,
		GUID:            randomGUID(),
		LZXOptions:      lzxOpts,
	})
	return err
}
