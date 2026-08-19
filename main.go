// nano11-go is a full, out-of-tree port of nano11builder.ps1
// (github.com/ntdevlabs/nano11) onto the gowim libraries
// (github.com/Pandapip1/gowim/*): it reproduces nano11's entire debloat
// pass against a real Windows install.wim edition -- AppX removal,
// servicing-package removal, the aggressive WinSxS wipe-to-allowlist,
// misc file deletions, and the full registry tweak set, including the
// steps nano11's own comments flag as breakage-prone -- using only
// gowim's Go packages: no DISM, no mounted image, no admin rights.
//
// It deliberately does NOT implement ISO9660/UDF writing or El Torito boot
// catalogs itself (see gowim's TODO.md: that subsystem is explicitly out
// of scope for gowim). Extracting/rebuilding the ISO around this tool's
// modified install.wim is left to external tooling (7z to extract,
// genisoimage to rebuild) driven by rebuild-iso.sh, not Go code.
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
	skipAppx           bool
	skipPackages       bool
	skipFileCleanup    bool
	skipWinSxSWipe     bool
	skipRegTweaks      bool
	skipServices       bool
	winRE              winREMode
	winREDonorPath     string
	bootWimPath        string
	bootWimOutPath     string
	skipBootRegTweaks  bool
	skipBootLocaleTrim bool
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
		"fastest":  lzx.Fastest,
		"fast":     lzx.Fast,
		"balanced": lzx.Balanced,
		"default":  lzx.DefaultOptions,
		"max":      lzx.Max,
	}
	p, ok := presets[s]
	if !ok {
		return fmt.Errorf("invalid -lzx-preset %q (want fastest, fast, balanced, default or max)", s)
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
	flag.BoolVar(&stages.skipAppx, "skip-appx", false, "skip provisioned AppX package removal")
	flag.BoolVar(&stages.skipPackages, "skip-packages", false, "skip servicing package removal by pattern")
	flag.BoolVar(&stages.skipFileCleanup, "skip-filecleanup", false, "skip aggressive manual file deletions")
	flag.BoolVar(&stages.skipWinSxSWipe, "skip-winsxs-wipe", false, "skip the WinSxS wipe-to-allowlist (the most aggressive, most likely-to-break-boot step)")
	flag.BoolVar(&stages.skipRegTweaks, "skip-regtweaks", false, "skip registry tweaks + autounattend.xml install")
	flag.BoolVar(&stages.skipServices, "skip-services", false, "skip service removal")
	flag.Var(winREModeFlag{&stages.winRE}, "winre-mode", "what to do to winre.wim during file cleanup: keep (default, safe) or donor-stub (graft real content from -winre-donor)")
	flag.StringVar(&stages.winREDonorPath, "winre-donor", "", "path to a real, extracted winre.wim to graft content from when -winre-mode=donor-stub")
	flag.StringVar(&stages.bootWimPath, "boot-wim", "", "path to boot.wim extracted from the ISO; if set, shrinks it to just the setup image (index 2), discarding the WinPE boot image")
	flag.StringVar(&stages.bootWimOutPath, "boot-wim-out", "", "path to write the shrunk boot.wim (required if -boot-wim is set)")
	flag.BoolVar(&stages.skipBootRegTweaks, "skip-boot-regtweaks", false, "skip the requirement-bypass/BitLocker registry tweaks applied to boot.wim's setup image")
	flag.BoolVar(&stages.skipBootLocaleTrim, "skip-boot-locale-trim", false, "skip removing non-en-US locale directories and their owning WinSxS packages from boot.wim's setup image")
	// Default "fast", not gowim's own ratio-first default: this tool's
	// workload is re-encoding multi-gigabyte images end to end (the
	// install.wim export pass alone re-encodes every blob of a ~7.4 GB
	// image, and the final write does it again), where the measured
	// tradeoff is lopsided. Measured 2026-08-18 on a 24-core x86-64
	// machine over a 4 MiB corpus compressed in 32 KiB chunks across all
	// cores (gowim lzx.Options's own doc has the corpus and full ladder):
	// fast 13.8 MB/s, balanced 2.94 MB/s (+0.52% size vs default),
	// default 0.511 MB/s, max 0.202 MB/s (-0.06%). Projected onto the real
	// 7.4 GB install.wim export that is ~20-30 min at fast against ~4 h at
	// default and ~10 h at max, for 2.87% more output.
	lzxPresetName := "fast"
	stages.lzx = lzx.Fast()
	flag.Var(lzxPresetFlag{&stages.lzx, &lzxPresetName}, "lzx-preset",
		"LZX encoder speed/size tradeoff for every WIM written: fastest, fast (default), balanced, default or max. "+
			"Measured 2026-08-18 (24-core, 4 MiB corpus, 32 KiB chunks, all cores): fast 13.8 MB/s at +2.87% output size, "+
			"balanced 2.94 MB/s at +0.52%, default 0.511 MB/s, max 0.202 MB/s at -0.06%. "+
			"For the real 7.4 GB install.wim export that is roughly 20-30 min (fast) vs ~1.5-2 h (balanced) vs ~4 h (default) vs ~10 h (max) of compression; "+
			"pick balanced or default when output size matters more than turnaround")
	flag.Parse()
	fmt.Printf("LZX preset: %s\n", lzxPresetName)

	if stages.bootWimPath != "" {
		if stages.bootWimOutPath == "" {
			log.Fatal("-boot-wim-out is required when -boot-wim is given")
		}
		fmt.Println("--- Shrinking boot.wim to just the setup image ---")
		if err := shrinkBootWim(stages.bootWimPath, stages.bootWimOutPath, stages.skipBootRegTweaks, stages.skipBootLocaleTrim, stages.lzx); err != nil {
			log.Fatalf("shrink boot.wim: %v", err)
		}
		fmt.Printf("Wrote shrunk boot.wim to %s\n", stages.bootWimOutPath)
	}

	if *wimPath == "" {
		if stages.bootWimPath != "" {
			return
		}
		log.Fatal("-wim is required")
	}

	if err := run(*wimPath, *outPath, *imageIndex, stages); err != nil {
		log.Fatal(err)
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
		if err := removeBloatAppx(r2, bt2, root, software, newBlobs); err != nil {
			return fmt.Errorf("remove appx: %w", err)
		}
	}

	if stages.skipPackages {
		fmt.Println("--- Skipping servicing package removal (-skip-packages) ---")
	} else {
		fmt.Println("--- Removing servicing packages (bloatware) ---")
		if err := removeBloatPackages(r2, bt2, root, languageCode); err != nil {
			return fmt.Errorf("remove packages: %w", err)
		}
	}

	if stages.skipFileCleanup {
		fmt.Println("--- Skipping aggressive file cleanup (-skip-filecleanup) ---")
	} else {
		fmt.Println("--- Performing aggressive manual file deletions ---")
		if err := runAggressiveFileCleanup(root, bt2, newBlobs, arch, stages.winRE, stages.winREDonorPath, stages.lzx); err != nil {
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
