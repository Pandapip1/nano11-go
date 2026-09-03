# GRUB boot menu for nano11-go ISOs (`-grub-efi` / `-grub-bios-hybrid`)

nano11-go can put a GRUB boot menu in front of the Windows installer instead of
booting Windows' boot manager directly. The menu:

- **autoprobes** attached devices for an already-installed / bootable OS and
  prefers it over the installer (so leaving the disc in doesn't trap you in
  Setup), excluding the install medium itself;
- shows a **5-second countdown** with the installer as the fallback default;
- boots **Windows Setup via wimboot** (WinPE is ramdisk-booted; Setup still
  reads `install.wim` from the disc);
- offers **memtest** and, under UEFI, a **reboot-into-firmware** entry.

Separately, nano11-go always stamps an **isohybrid MBR** into every ISO it
authors (on by default; `-skip-iso-hybrid-mbr` to disable) so the image is
also bootable when written byte-for-byte to a USB stick (`dd`, GNOME Disks'
"Restore Disk Image", Rufus in DD-image mode) — USB boot enumeration looks
for a GPT/MBR-partitioned disk, not an El Torito catalog, which is all a
plain ISO has. `-grub-bios-hybrid`/`-grub-bios-eltorito` extend that same
trick to legacy BIOS/CSM boot, using GRUB for the same reason as below.

The GRUB binaries are **not vendored** in this repo — build them with
[`build-grub.sh`](build-grub.sh):

```sh
contrib/grub/build-grub.sh
# -> contrib/grub/BOOTX64.EFI               (UEFI)
# -> contrib/grub/BOOT_HYBRID.img           (legacy BIOS isohybrid MBR template)
# -> contrib/grub/BIOS_ELTORITO_MENU.img    (legacy BIOS, same menu as UEFI)
# -> contrib/grub/BIOS_ELTORITO_SILENT.img  (legacy BIOS, silent straight-to-Setup)

# UEFI menu + legacy BIOS, same menu on both:
nano11-go ... -iso-dir <tree> -iso-out out.iso \
  -grub-efi contrib/grub/BOOTX64.EFI \
  -grub-bios-hybrid contrib/grub/BOOT_HYBRID.img \
  -grub-bios-eltorito contrib/grub/BIOS_ELTORITO_MENU.img

# Default (no menu) ISO, but still legacy-BIOS-USB-bootable:
nano11-go ... -iso-dir <tree> -iso-out out.iso \
  -grub-bios-hybrid contrib/grub/BOOT_HYBRID.img \
  -grub-bios-eltorito contrib/grub/BIOS_ELTORITO_SILENT.img
```

nano11-go wraps the EFI application in a small FAT boot image (built in Go, see
`fatimg.go`) and points the ISO's UEFI El Torito entry at it. The BIOS El
Torito entry, if `-grub-bios-hybrid`/`-grub-bios-eltorito` are given, is
replaced with the supplied GRUB image; otherwise it stays Windows' native
`etfsboot.com` (optical/VM BIOS boot only — see "Why GRUB is mandatory for
BIOS-mode USB boot" below for why `etfsboot.com` itself can never gain that).

## Why a patched GRUB

It has to be [a1ive's GRUB fork](https://github.com/a1ive/grub), not stock GRUB.
Only a1ive's `wimboot` module (`grub-core/map/wimboot/` — a module distinct
from `map` itself, easy to conflate, and this doc did for a while) provides a
`wimboot` command that reads `boot.wim` through GRUB's own filesystem drivers.
Stock GRUB **cannot** launch the Windows installer from optical media by any
route:

- chainloading `bootmgr`/`cdboot` from the disc: they load and start, then exit
  — they need the El Torito/`cdboot` environment firmware normally provides;
- chainloading upstream `wimboot`: it runs but `OpenProtocol(DeviceHandle,
  SimpleFileSystem)` returns `EFI_UNSUPPORTED` — GRUB hands it a BlockIO handle
  with no filesystem protocol (true for iso9660 *and* UDF).

Stock GRUB only launches such a payload from a real FAT volume, i.e. USB/disk
boot — not an optical ISO. a1ive's `wimboot` sidesteps this by not using EFI's
SimpleFileSystem at all.

## Why GRUB is mandatory for BIOS-mode USB boot

Windows' own `etfsboot.com` cannot be patched or wrapped into working here at
all, on any path — not just the GRUB-menu one. It's built specifically for
the El Torito CD-emulation environment (it makes El Torito-specific BIOS
calls assuming a virtual CD-ROM is active) and has no concept of finding or
chainloading `bootmgr` from a raw disk by LBA. Raw-USB BIOS boot has no CD
emulation at all: the firmware just loads the MBR sector and runs it as
ordinary x86 code. So GRUB has to be the BIOS bootstrap whenever legacy-BIOS
USB boot is wanted, on the default (no `-grub-efi`) ISO exactly as much as
the GRUB-menu one — `-grub-bios-eltorito`'s `_SILENT.img` variant exists for
exactly that: same GRUB bootstrap, just wimboot-ing straight to Setup with
no menu, matching what the default ISO's UEFI path already does today.

## Requirements / caveats

- **Secure Boot must be OFF** for the UEFI path — this GRUB is unsigned.
- Legacy BIOS boot needs the CSM/legacy option enabled in firmware that
  defaults to UEFI-only; both boot modes work from the same ISO/USB stick.
- Building GRUB needs `bison` and `flex` in addition to gcc/autotools, and
  `tar` for the BIOS build's memdisk image.
- Edit [`grub.cfg`](grub.cfg) (the menu, used by both platforms) or
  [`grub-silent.cfg`](grub-silent.cfg) (the default path's BIOS-only silent
  boot) to tune behavior; both are embedded at build time, so re-run
  `build-grub.sh` after changing either.
- `-O i386-pc-eltorito`'s core image has a hard ~480 KiB ceiling (a real
  historical BIOS real-mode memory constraint enforced at GRUB build time),
  so the BIOS module list is trimmed relative to the EFI one — no
  `all_video`, so BIOS mode boots in plain VGA text rather than `gfxterm`.
