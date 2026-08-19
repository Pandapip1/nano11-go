package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/service"
	"github.com/Pandapip1/gowim/wim"
)

// shrinkBootWim ports nano11builder.ps1's "Shrinking boot.wim..." step
// (lines 490-534): boot.wim normally ships with 2 images (a WinPE boot
// image, index 1, and the actual Windows Setup image, index 2); the PS1
// script exports only index 2 into a new boot.wim, discarding the WinPE
// image entirely, since Setup itself only ever boots index 2.
//
// The PS1 script does its export/registry-edit/re-export as two DISM
// /Export-Image passes around an intervening mount; this port instead
// exports once to an in-memory image, edits the registry hives directly
// (mirroring main.go's own install.wim flow), and writes the final result
// in one pass.
//
// Both the WinPE-image drop and the non-en-US locale/WinSxS trim below
// looked like size losses/no-ops when measured against the untouched
// source boot.wim (see TODO.md) -- but that was the wrong baseline. A
// registry-tweak pass (unless skipRegTweaks is set) forces a full
// decompress/re-encode of boot.wim regardless of anything else this
// function does, since wim.WriteTo always re-encodes every blob (see
// export.go's doc comment). Measured against the real alternative --
// re-encoding boot.wim as-is, both images, nothing trimmed -- dropping the
// WinPE image saves a real ~13.5MB and the locale/WinSxS trim saves a
// further ~7.9MB on top of that (real numbers from a stock Windows 11
// 25H2 boot.wim, see TODO.md). Both are genuine wins once a re-encode is
// already happening for another reason; neither is worth forcing a
// re-encode on its own merits alone.
func shrinkBootWim(bootWimPath, outPath string, skipRegTweaks, skipLocaleTrim bool, lzxOpts lzx.Options) error {
	f, err := os.Open(bootWimPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", bootWimPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("read %s: %w", bootWimPath, err)
	}
	bt, err := r.BlobTable()
	if err != nil {
		return err
	}
	xmlData, err := r.XMLData()
	if err != nil {
		return err
	}
	if len(xmlData.Images) < 2 {
		return fmt.Errorf("%s has %d image(s), want at least 2 (WinPE + setup)", bootWimPath, len(xmlData.Images))
	}

	ctype, err := r.Header().CompressionType()
	if err != nil {
		return fmt.Errorf("%s: %w", bootWimPath, err)
	}
	chunkSize := r.Header().ChunkSize

	tmpExport := outPath + ".export.tmp"
	if err := exportBootSetupImage(r, bt, xmlData, tmpExport, ctype, chunkSize, lzxOpts); err != nil {
		return fmt.Errorf("export setup image from %s: %w", bootWimPath, err)
	}
	defer os.Remove(tmpExport)

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
		return fmt.Errorf("expected exactly 1 image in the boot.wim export, got %d", len(metaResources))
	}
	meta, err := r2.ImageMetadata(metaResources[0])
	if err != nil {
		return err
	}

	newBlobs := map[wim.Hash][]byte{}

	if skipRegTweaks {
		fmt.Println("--- Skipping boot.wim registry bypass tweaks (-skip-boot-regtweaks) ---")
	} else {
		hs, err := registry.LoadHiveSet(r2, meta.Root, bt2)
		if err != nil {
			return fmt.Errorf("load boot.wim hive set: %w", err)
		}
		if err := applyBootWimRegistryTweaks(hs); err != nil {
			return fmt.Errorf("apply boot.wim registry tweaks: %w", err)
		}
		for name, h := range hs.Hives {
			nb, err := h.Save(bt2)
			if err != nil {
				return fmt.Errorf("save boot.wim %s hive: %w", name, err)
			}
			if nb.Data != nil {
				newBlobs[nb.Hash] = nb.Data
			}
		}
	}

	if skipLocaleTrim {
		fmt.Println("--- Skipping boot.wim locale/WinSxS trim (-skip-boot-locale-trim) ---")
	} else {
		trimmed := trimBootWimNonEnUSLocaleContent(meta, bt2)
		fmt.Printf("Trimmed %d non-en-US locale/WinSxS-package directories from boot.wim setup image\n", trimmed)
	}

	rebuiltBT, err := wim.RebuildBlobTable([]*wim.ImageMetadata{meta}, bt2)
	if err != nil {
		return fmt.Errorf("rebuild boot.wim blob table: %w", err)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	src := combinedBlobSource{overrides: newBlobs, fallback: wim.NewReaderBlobSource(r2, bt2)}
	_, err = wim.WriteTo(out, []*wim.ImageMetadata{meta}, rebuiltBT, xmlData2, src, wim.WriteOptions{
		CompressionType: ctype,
		ChunkSize:       chunkSize,
		BootIndex:       1,
		GUID:            randomGUID(),
		LZXOptions:      lzxOpts,
	})
	if err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

// exportBootSetupImage exports just image index 2 (Windows Setup) from a
// boot.wim to dest, preserving the source's own compression type/chunk
// size (matching the PS1 script's first export pass, which passes no
// /compress flag at all).
func exportBootSetupImage(r *wim.Reader, bt *wim.BlobTable, xmlData *wim.XMLData, dest string, ctype wim.CompressionType, chunkSize uint32, lzxOpts lzx.Options) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	const setupImageIndex = 2
	_, err = wim.ExportImage(r, bt, xmlData, []int{setupImageIndex}, f, wim.WriteOptions{
		CompressionType: ctype,
		ChunkSize:       chunkSize,
		GUID:            randomGUID(),
		LZXOptions:      lzxOpts,
	})
	return err
}

// bootWimLocaleDirRe matches a directory named after a Windows locale/
// language tag (e.g. "de-DE", "fr-FR", or the 2-letter-only form some
// components use), anywhere in the tree.
var bootWimLocaleDirRe = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// bootWimWinSxSLocaleSuffixRe matches a WinSxS package directory name's
// trailing locale-suffix segment, e.g.
// "...-bootmanager-efi.resources_31bf3856ad364e35_10.0.26100.7920_de-de_<hash>".
var bootWimWinSxSLocaleSuffixRe = regexp.MustCompile(`_([a-z]{2}-[a-z]{2})_[0-9a-f]{16}$`)

// trimBootWimNonEnUSLocaleContent removes, from meta's tree, every
// non-en-US locale-named directory (System32\de-DE, Boot\EFI\fr-FR, etc.)
// and every non-en-US locale-suffixed WinSxS package directory, returning
// the number of directories removed.
//
// This is NOT a port of anything in nano11builder.ps1 -- the PS1 script
// never touches boot.wim's file tree, only install.wim's (via its
// language-feature servicing-package removal, which this package already
// ports in appx.go/packages.go). It exists because a real-data survey (see
// TODO.md) found that removing just the visible locale-named directories
// reclaims almost nothing: every one of those files is hard-linked (same
// blob hash) to a second copy in a locale-suffixed WinSxS package
// directory, so the visible copy's removal alone only decrements a
// blob-table RefCount that stays above zero. Both copies have to go
// together for wim.RebuildBlobTable to actually drop the blob.
//
// Like install.wim's own WinSxS-adjacent cleanup, this removes directories
// only -- it does not touch or validate any WinSxS manifest/catalog
// metadata that might reference these packages (COMPONENTS hive package
// registration, deployment manifests). That risk was verified acceptable
// for this specific case via a real QEMU/OVMF boot test of the resulting
// boot.wim (see TODO.md for the result) before this function was wired in
// unconditionally; it is not a general license to delete WinSxS content
// elsewhere (e.g. install.wim) without the same kind of real verification.
func trimBootWimNonEnUSLocaleContent(meta *wim.ImageMetadata, bt *wim.BlobTable) int {
	var toRemove []struct {
		path  string
		entry *wim.DirEntry
	}

	var walk func(d *wim.DirEntry, path string)
	walk = func(d *wim.DirEntry, path string) {
		for _, c := range d.Children {
			p := path + `\` + c.NameUTF8()
			if c.IsDirectory() && bootWimLocaleDirRe.MatchString(c.NameUTF8()) && !strings.EqualFold(c.NameUTF8(), "en-US") {
				toRemove = append(toRemove, struct {
					path  string
					entry *wim.DirEntry
				}{p, c})
				continue
			}
			if c.IsDirectory() {
				walk(c, p)
			}
		}
	}
	walk(meta.Root, "")

	if winsxs, err := meta.Root.Lookup(`Windows\WinSxS`); err == nil {
		for _, c := range winsxs.Children {
			if !c.IsDirectory() {
				continue
			}
			m := bootWimWinSxSLocaleSuffixRe.FindStringSubmatch(c.NameUTF8())
			if m != nil && m[1] != "en-us" {
				toRemove = append(toRemove, struct {
					path  string
					entry *wim.DirEntry
				}{`Windows\WinSxS\` + c.NameUTF8(), c})
			}
		}
	}

	for _, item := range toRemove {
		decrementBlobRefs(bt, item.entry)
		if err := meta.Root.Remove(item.path); err != nil {
			// Already accounted for in toRemove via a live tree walk, so
			// removal cannot fail; a panic here would indicate a real bug.
			panic(fmt.Sprintf("trimBootWimNonEnUSLocaleContent: remove %s: %v", item.path, err))
		}
	}

	return len(toRemove)
}

// applyBootWimRegistryTweaks ports nano11builder.ps1's "Bypassing system
// requirements (on the system image)" section (lines 504-520): re-applies
// the requirement-bypass and BitLocker tweaks against boot.wim's own
// DEFAULT/NTUSER.DAT/SYSTEM hives (already-loaded, e.g. from an exported
// setup image), not just install.wim's. Windows Setup itself runs from
// boot.wim, so these checks are actually evaluated against boot.wim's
// hives at Setup time, not install.wim's -- applying them only to
// install.wim (as regtweaks.go's applyRegistryTweaks already does) leaves
// the Setup-phase checks themselves unbypassed.
//
// Unlike the PS1 script (which hardcodes ControlSet001), this resolves
// CurrentControlSet the same way regtweaks.go does, since hardcoding is
// only valid because WinPE images conventionally have just one control
// set -- resolving it properly costs nothing and doesn't rely on that
// convention holding.
func applyBootWimRegistryTweaks(hs *registry.HiveSet) error {
	def := hs.Hives[registry.HiveDefault]
	ntuser := hs.Hives[registry.HiveDefaultUser]
	sys := hs.Hives[registry.HiveSystem]
	if def == nil || ntuser == nil || sys == nil {
		return fmt.Errorf("boot.wim setup image is missing one of DEFAULT/NTUSER.DAT/SYSTEM")
	}
	system := sys.Hive.Root

	ccs, err := service.CurrentControlSet(system)
	if err != nil {
		return fmt.Errorf("resolve CurrentControlSet: %w", err)
	}

	fmt.Println("Bypassing system requirements (on the boot.wim setup image)...")
	dw(def.Hive.Root.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV1", 0)
	dw(def.Hive.Root.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV2", 0)
	dw(ntuser.Hive.Root.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV1", 0)
	dw(ntuser.Hive.Root.FindOrCreatePath(`Control Panel\UnsupportedHardwareNotificationCache`), "SV2", 0)
	labConfig := system.FindOrCreatePath(`Setup\LabConfig`)
	dw(labConfig, "BypassCPUCheck", 1)
	dw(labConfig, "BypassRAMCheck", 1)
	dw(labConfig, "BypassSecureBootCheck", 1)
	dw(labConfig, "BypassStorageCheck", 1)
	dw(labConfig, "BypassTPMCheck", 1)
	dw(system.FindOrCreatePath(`Setup\MoSetup`), "AllowUpgradesWithUnsupportedTPMOrCPU", 1)

	fmt.Println("Disabling BitLocker device encryption (on the boot.wim setup image)...")
	dw(ccs.FindOrCreatePath(`Control\BitLocker`), "PreventDeviceEncryption", 1)

	return nil
}
