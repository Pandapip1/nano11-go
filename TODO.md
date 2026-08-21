# TODO

Gaps found 2026-08-17 comparing nano11-go against the original
`nano11builder.ps1` (cached at `/tmp/claude/repos/nano11/nano11builder.ps1`).
The install.wim side of the port (AppX, servicing packages, file cleanup,
WinSxS wipe, registry tweaks, service removal) is fully ported 1:1 already --
these are the only remaining gaps, all outside install.wim itself.

- [x] **boot.wim slimming.** PS1 lines 490-534: exports only image index 2
      (setup) out of boot.wim's normal 2 images (WinPE + setup), discarding
      the WinPE image entirely. Done: `bootwim.go`'s `shrinkBootWim`, wired
      into `main.go` via `-boot-wim`/`-boot-wim-out` (independent of
      `-wim`/`-image`, since it doesn't touch install.wim at all). Uses the
      same `wim.ExportImage` machinery as the existing install.wim export
      step, preserving the source's own compression type/chunk size.
      **Measured against a real stock Windows 11 25H2 boot.wim (614.8MB, 2
      images), this alone does NOT reduce size versus the untouched
      original** -- the first pass here even measured the drop-WinPE output
      as slightly *larger* (639.9MB). Root cause, confirmed by directly
      comparing referenced-blob hash sets between the two images: image 2
      (setup) shares 13460/13815 (97.4%) of its referenced blobs with image
      1 (WinPE) -- the setup image is built as a superset of the WinPE
      image, not a separate payload -- so the blobs unique to image 1
      (reclaimable by dropping it) are only ~11.5MB out of the file's
      613.4MB total blob-table size, and `wim.WriteTo`/`ExportImage`'s
      unconditional full re-encode (see `export.go`'s own doc comment)
      measured ~4% less efficient than whatever produced the original file,
      more than erasing that ~11.5MB.

      **That comparison was against the wrong baseline, though.** Applying
      the registry-bypass tweaks below (item 3) already forces a full
      re-encode of boot.wim regardless of whether the WinPE image is kept
      -- there is no way to apply those tweaks without paying the re-encode
      cost anyway. The real alternative to "drop WinPE" is not "leave
      boot.wim untouched", it's "re-encode boot.wim with both images kept".
      Measured that comparison directly, both built with the same
      fixed/non-random GUID for a clean diff: re-encoding both images
      unchanged is 653,977,434 bytes; re-encoding with only image 2 (setup)
      kept is 639,851,144 bytes -- a real, verified **13.5MB saved** once
      the re-encode is already happening. Combined with the locale/WinSxS
      trim below (item below), the same re-encode drops to 631,984,057
      bytes -- ~22MB saved total versus the correct "re-encode, change
      nothing" baseline, even though the end result is still ~17MB larger
      than the pristine, untouched, never-re-encoded original (that
      residual gap is the LZX re-encode inefficiency itself -- see the new
      gowim-side investigation item this prompted). Net: implemented,
      wired in by default, and a genuine size win given that the
      registry-tweaks step (which most users doing this at all will want)
      already requires a full rewrite.
- [x] **Trim non-en-US locale content and its owning WinSxS packages from
      boot.wim.** Since dropping
      the whole WinPE image (item above) turned out not to help -- image 2
      already needs ~97.4% of the same blobs -- the more promising angle is
      trimming *within* the setup image's own file tree instead (the same
      kind of per-file/per-directory removal `filecleanup.go` already does
      against install.wim), e.g. unused language resources, drivers, or
      other WinPE payload not needed for Setup to run.

      **Surveyed against the same real boot.wim used above (image 2, 21213
      files/2.55GB uncompressed tree).** Found ~266 locale-named directories
      (`xx-XX`/`xx` dirs at various depths: `System32\de-DE`,
      `Boot\EFI\fr-FR`, `Speech\Engines\TTS\ja-JP`, etc.) totaling ~35.6MB
      uncompressed across 229 non-en-US languages. This looked like the same
      kind of win the PS1's own AppX language-feature removal already
      captures for install.wim (PS1 lines 116-120) -- **but a first real
      test of removing just those directories reclaimed only ~25KB**, not
      megabytes. Root cause, confirmed by hashing individual files and
      searching the whole image-2 tree for other dentries sharing the same
      blob hash: every one of these "visible" localized resource files
      (`bootmgfw.efi.mui`, `bootmgr.efi.mui`, `memtest.efi.mui`, etc.) is
      hard-linked (same blob hash, blob-table `RefCount` > 1) to a second
      copy living in a locale-suffixed `WinSxS` package dir (e.g.
      `WinSxS\amd64_...-bootmanager-efi.resources_..._de-de_<hash>`).
      Deleting only the mirror directory just decrements a refcount that
      stays above zero because WinSxS still references the same blob --
      `wim.RebuildBlobTable` correctly keeps it, so nothing is actually
      reclaimed.

      Measured the real lever instead: image 2's `WinSxS` contains 1439
      locale-suffixed package dirs; non-en-US ones total ~40.1MB uncompressed
      (`en-us`'s own is ~26MB and stays). **Removing both the mirror
      directories *and* their owning WinSxS package dirs together (890 dirs
      total) reclaimed a real, measured 7,869,079 bytes (~7.5MB)** on top of
      an otherwise-identical single-image re-export (639,853,136 bytes bare
      export vs. 631,984,057 bytes trimmed -- both built with the same
      fixed/non-random GUID so the comparison is apples-to-apples). Like the
      WinPE-image drop above, this is only a genuine net win layered on top
      of a re-encode that's already happening for another reason -- it does
      not make boot.wim handling a net size win versus never touching
      boot.wim at all, only versus the real "re-encode and change nothing"
      alternative.

      **Boot-tested before being wired in unconditionally.** Deleting WinSxS
      package directories without any corresponding manifest/catalog update
      is the same class of WinSxS-internals risk gowim's own TODO.md already
      flags as unverified (DriverStore-hash/WinSxS section), and
      `wimlib-imagex verify` passing on the trimmed file is not equivalent to
      Setup actually mounting and booting it. Built the real pipeline output
      (`shrinkBootWim` with both the image-drop and this trim applied,
      890 dirs removed, 629,829,395 bytes, `wimlib-imagex verify` clean),
      swapped it into a real extracted retail ISO's `sources/boot.wim`
      (install.wim left untouched, since this test targets boot.wim/Setup's
      own mount path, not the install image), and booted it in QEMU
      (q35+KVM+OVMF UEFI, matching this project's existing test
      methodology). Setup's WinPE environment mounted and rendered correctly
      -- reached the "Select language settings" screen, and a real keyboard
      interaction (Enter) correctly advanced to a distinct second screen
      ("Product key") with correct layout/fonts/images, confirming the trim
      didn't corrupt anything the early Setup UI path depends on. Did not
      run a full install (that exercises install.wim's own correctness, not
      boot.wim's), so later-stage boot.wim-dependent steps (SafeOS/WinRE
      re-mount during actual OS deployment) remain unverified by this test
      specifically -- but the acute risk this item existed to check (does
      cutting WinSxS-hardlinked locale content break Setup's own boot/mount
      path) is now verified, not assumed. Implemented as
      `trimBootWimNonEnUSLocaleContent` in `bootwim.go`, on by default,
      skippable via `-skip-boot-locale-trim` for symmetry with the other
      boot.wim steps.
- [x] **boot.wim registry bypass tweaks.** PS1 lines 504-520: re-mounts
      boot.wim's setup image (index 2) and re-applies a subset of the same
      registry tweaks already ported for install.wim in `regtweaks.go` --
      SV1/SV2, LabConfig Bypass* (TPM/SecureBoot/RAM/CPU checks), BitLocker
      PreventDeviceEncryption. Done: `bootwim.go`'s
      `applyBootWimRegistryTweaks`, folded into `shrinkBootWim`'s pipeline
      (skippable via `-skip-boot-regtweaks`) since it needs the same
      exported-setup-image hive set as the shrink step. Unlike the PS1
      (which hardcodes `ControlSet001`), resolves `CurrentControlSet` via
      `service.CurrentControlSet` like `regtweaks.go` already does.
      Verified against the real boot.wim used for the shrink-step test
      above: ran end to end, `wimlib-imagex verify` passed, and the written
      SYSTEM hive was re-parsed directly (via `regf.Parse`) to confirm
      `BypassCPUCheck`/`BypassRAMCheck`/`BypassSecureBootCheck`/
      `BypassStorageCheck`/`BypassTPMCheck`/`PreventDeviceEncryption` all
      read back as `1`. Negligible size impact; Setup-phase requirement
      bypass UX only.
- [x] **Final ISO-root cleanup.** PS1 lines 541-547: deletes everything at
      the extracted-media root except `boot`, `efi`, `sources`, `bootmgr`,
      `bootmgr.efi`, `setup.exe`, `autounattend.xml`. Done: originally in
      `rebuild-iso.sh`; since 2026-08-19 in Go, as `isoimage.go`'s
      `cleanISORoot` against the same `isoRootKeepList`, run just before
      the ISO is authored. Verified against the actual shape of a real
      extracted retail ISO root (`boot`, `efi`, `sources`, `support`,
      `autorun.inf`, `bootmgfw.efi`, `bootmgr`, `bootmgr.efi`, `setup.exe`)
      via a disposable mock directory with the same entries (not run
      against the real working `isox` tree, to avoid destructively
      touching in-progress work): correctly dropped `autorun.inf`,
      `bootmgfw.efi` (the real one lives under `efi/microsoft/boot/`, not
      the root), and `support/`, while preserving everything on the keep
      list. Measured against the real media on disk: negligible size
      impact here (`support/` 480KB, `bootmgfw.efi` 2.9MB, `autorun.inf`
      4KB -- ~3.4MB total), though this may vary on other source media
      (e.g. ISOs shipping extra language packs or tools at the root).

Confirmed still-accurate non-goals (not new, just re-confirmed against the
current PS1 during this gap analysis): install.wim -> install.esd final
recompression (gowim's LZMS encoder is unverified against an independent
decoder) and DISM `/Cleanup-Image /StartComponentCleanup /ResetBase` (no
offline equivalent, undocumented COMPONENTS-hive-internal accounting -- see
gowim's own TODO.md for the full research writeup).

## 2026-08-19 validation findings (aggressive-removal additions)

**CONFIRMED REGRESSION: current build fails OOBE ("boot to desktop").** A
clean, hands-off QEMU install (q35+KVM+OVMF, fresh disk, no key input during
OOBE) of the current build reaches the file-copy phase and the post-install
lock screen, then fails in the oobeSystem pass with the Windows OOBE-recovery
screen: "Why did my PC restart? There's a problem that's keeping us from
getting your PC ready to use" (it asks to connect to a network to download a
repair update -- which it cannot, since the vendor NIC drivers are removed).
This was reproduced WITHOUT the earlier key-mashing confound, so it is
attributable to the debloat, not to test interference. The install.wim
file-copy itself is fine (explorer.exe present, files applied); the failure
is specifically OOBE finalization.

**BISECT COMPLETE -- root cause is the web-engine removal (mshtml/edgehtml).**
A three-build QEMU ladder (each a clean hands-off install to the desktop):
  - current (NIC + web engines + Defender/Search all removed): FAILS OOBE.
  - all-keep (all three reverted): reaches full desktop.
  - web-only (web engines KEPT, but NIC drivers AND Defender/Search still
    removed): reaches full desktop.
web-only keeps only mshtml.dll/edgehtml.dll yet passes, while the 242 MB of
vendor NIC drivers and the Defender/Search packages stay removed -- so those
are confirmed OOBE-safe and the break is the four web-engine files alone.
Cause: Windows 11 OOBE (CloudExperienceHost) is HTML-hosted and renders
through Trident/EdgeHTML.

FIXED: the web-engine removal is now OFF by default. The flag was inverted
from `-keep-web-engines` (default remove) to `-remove-web-engines` (default
keep), documented in filecleanup.go as breaking OOBE. `-keep-nic-drivers`
and `-keep-defender-search` remain default-off (their removals are OOBE-safe;
those flags exist for hardware-compatibility / Defender-retention choices,
not correctness). The 89 MB web-engine cut is the price of a bootable image.

**Servicing-package removal is ~worthless for size (measured).** A blob-level
measurement harness (measure_test.go, run against real 25H2 build 26200 Pro)
shows every servicing-package pattern reclaims only its .mum bytes -- the
payload lives in WinSxS (wiped anyway) or hardlinked System32 copies. Totals:
all virtualization patterns (Hyper-V/WSL/Containers, 460 packages) ~85 KB;
the revived Defender+Search patterns (43 packages) ~30 KB. The revival is
therefore almost pure breakage risk for no size benefit -- hence gated behind
-keep-defender-search, and a candidate to default to ON (keep).

**winre.wim donor-stub VERIFIED (635 MB, the biggest single win).**
`-winre-mode=donor-stub` was run end to end and reached the Windows logon
screen in QEMU -- Setup's entire SafeOS phase (extract/mount/file-copy/
commit) now passes. install.wim drops from 3,286,450,368 (winre-keep) to
2,651,137,264 (donor-stub), a real 635.3 MB. Combined with the rest of the
2026-08-19 file cleanup, install.wim = 2,317,296,126. This is the highest-
value size change in the project and should be considered for default-on once
the OOBE regression above is resolved (they are independent: winre-keep also
hits the OOBE failure).

---

## 2026-08-20 -- winre-stub now default; AI-removal bisected + reverted to opt-in

Two changes shipped and validated in QEMU (q35+KVM+OVMF, clean installs).

**winre donor-stub is now DEFAULT (`-winre-mode` defaults to `donor-stub`).**
The donor is auto-sourced from the image's *own* winre.wim at cleanup time
(extractWinREDonorToTemp reads `\Windows\System32\Recovery\winre.wim`, writes
it to a temp file, grafts its real `\Windows\Boot` subtree into the stub), so
no external `-winre-donor` is needed. `-winre-mode=keep` opts back out.
Validated: the default build installed through Setup's full SafeOS phase and
booted into OOBE (region/keyboard pages rendered) in two separate runs.
Winre.wim inside install.wim: 672,970,588 -> 29,978,979 bytes.

**AI-removal (`-remove-ai`) BREAKS OOBE -- reverted to opt-in, default OFF.**
Added a Windows-AI removal stage (CoreAI overlay ~19 MB + DirectML `.mun`
resources ~21 MB + WUModels ~1 MB). A three-way test settled it:
  - build with AI removed  (winre stub on) -> OOBE FAILS at the "Why did my
    PC restart? There's a problem that's keeping us from getting your PC
    ready to use" recovery screen (shots_final/FAILURE_oobe_why_restart.png).
  - build with AI KEPT     (winre stub on) -> OOBE renders and navigates
    normally: region -> keyboard pages (shots_ka/PROOF_oobe_region_ok.png,
    PROOF_oobe_keyboard_ok.png).
Since winre.wim plays no part in OOBE and is identical between the two builds,
the break is the AI removal -- specifically CoreAI, which turns out to be an
OOBE-integrated SystemApp in 25H2 (same failure mode as the web engines), not
the self-contained feature app first assumed. Only ~42 MB / 27 MB-on-ISO, so
not worth an OOBE break: the removal is now behind `-remove-ai` (default keep,
documented as breaking OOBE), mirroring `-remove-web-engines`. The DirectML
`.mun` + WUModels subset (~22 MB) is very likely OOBE-safe on its own but has
not been separately install-tested, so it rides the same opt-in flag rather
than shipping by default.

**Shippable, validated default now: ISO 3,220,226,048 bytes (3.0 GB), from
the 3.6 GB webonly build** -- the winre stub is the whole win. install.wim
2.2 GB. ISOs: nano11go_keepai.iso (validated default), nano11go_final_ai.iso
(AI-removed, OOBE-broken, kept only as the bisect's negative control).

---

## 2026-08-20 (later) -- HTML/AI split into essential vs non-essential

The two all-or-nothing OOBE-breaking cuts (web engines, AI) are now each split
into two tiers, and the non-essential tier is removed BY DEFAULT (validated
OOBE-safe), while the essential tier stays behind its opt-in flag.

HTML engines: a QEMU bisect settled which engine OOBE needs. Removing BOTH
mshtml+edgehtml breaks OOBE; removing ONLY mshtml does NOT (OOBE reached the
region page). So:
  - NON-ESSENTIAL: mshtml.dll (Trident, ~43 MB) -- removed by default.
  - ESSENTIAL: edgehtml.dll (EdgeHTML, ~46 MB) -- CloudExperienceHost renders
    through it; kept unless -remove-web-engines.

AI: from the 2026-08-20 bisect:
  - NON-ESSENTIAL: DirectML .mun resources (~21 MB) + WUModels (~1 MB) --
    removed by default.
  - ESSENTIAL: CoreAI SystemApp (~19 MB), OOBE-integrated -- kept unless
    -remove-ai.

Validated: a build with only the non-essential tier removed (mshtml +
DirectML.mun + WUModels; edgehtml + CoreAI kept; winre stubbed) installed and
reached the OOBE region/keyboard pages (shots_split/PROOF_oobe_region_ok.png).
The -remove-web-engines / -remove-ai flags now remove only the essential
remainder on top of the default non-essential removal (net effect unchanged
for anyone who set them: everything still comes out).

Default ISO now 3,193,608,192 bytes (nano11go_split.iso), ~27 MB below the
AI-kept build, from the non-essential removal -- on top of the winre stub's
606 MB. install.wim 2.2 GB.

---

## 2026-08-20 -- Optional GRUB boot menu (-grub-efi)

Added an opt-in GRUB front-end to the authored ISO: -grub-efi <path-to-EFI>
swaps the UEFI El Torito boot image from Windows' efisys_noprompt.bin to a FAT
image holding the given GRUB application at \EFI\BOOT\BOOTX64.EFI. The menu
autoprobes attached devices for an installed OS (prefers it over the installer,
excludes the disc), 5 s countdown with Windows Setup as fallback default, boots
Setup via wimboot, plus memtest and a UEFI reboot-into-firmware entry.

Must be a1ive's GRUB fork -- verified from source that STOCK GRUB cannot launch
the Windows installer from optical media by ANY route:
  - chainload bootmgr/cdboot from the disc: LoadImage+StartImage succeed, then
    bootmgr exits (needs the El Torito/cdboot environment).
  - chainload upstream wimboot: runs but OpenProtocol(DeviceHandle,
    SimpleFileSystem) = EFI_UNSUPPORTED; GRUB hands it a BlockIO handle with no
    SFS. True for iso9660 AND UDF. wimboot-EFI reads files only via SFS
    (src/efimain.c). Stock GRUB only works from a real FAT volume (USB/disk).
a1ive's `map` module `wimboot` reads boot.wim through GRUB's own file layer, so
it works from optical. See contrib/grub/ and the grub-wimboot-optical-boot
memory note for the full investigation and build recipe.

Implementation:
  - fatimg.go: a minimal pure-Go FAT16 writer (one file, \EFI\BOOT\BOOTX64.EFI,
    all 8.3 names -- no LFN). Keeps the no-external-tools property; no mtools.
    Round-trip verified against mdir/mtype.
  - isoimage.go: buildISO stages the FAT image at boot/grub/efi.img and points
    the UEFI boot entry at it; BIOS entry stays Windows-native etfsboot.com.
  - contrib/grub/: grub.cfg (the menu), build-grub.sh (builds a1ive GRUB and
    grub-mkstandalone's the EFI -- binary NOT vendored), README.md.

Caveats: UEFI only, Secure Boot must be OFF (unsigned GRUB). BIOS boot is still
Windows-native (an i386-pc GRUB build would be needed to front BIOS too).

Validated end-to-end 2026-08-20 in QEMU/OVMF (SB off): the ISO authored by the
nano11go binary itself (Go-built FAT image + gowim UDF writer) auto-booted
through the GRUB menu into Windows 11 Setup (language page) from a CD-ROM, no
keypress. Earlier hand-built probe reached the edition/requirements stage,
confirming install.wim is read via CDFS.
