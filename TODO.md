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

Bisect scaffolding added for this: `-keep-nic-drivers`, `-keep-web-engines`,
`-keep-defender-search` (main.go), each isolating one 2026-08-19 addition.
Leading hypothesis: web engines (mshtml/edgehtml) -- OOBE's
CloudExperienceHost renders via HTML. Bisect in progress; do NOT ship the
current default until the OOBE-breaking removal is identified and either
fixed or moved behind a default-off flag.

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
