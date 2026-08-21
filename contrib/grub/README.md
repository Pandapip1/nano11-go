# GRUB boot menu for nano11-go ISOs (`-grub-efi`)

nano11-go can put a GRUB boot menu in front of the Windows installer instead of
booting Windows' boot manager directly. The menu:

- **autoprobes** attached devices for an already-installed / bootable OS and
  prefers it over the installer (so leaving the disc in doesn't trap you in
  Setup), excluding the install medium itself;
- shows a **5-second countdown** with the installer as the fallback default;
- boots **Windows Setup via wimboot** (WinPE is ramdisk-booted; Setup still
  reads `install.wim` from the disc);
- offers **memtest** and, under UEFI, a **reboot-into-firmware** entry.

The GRUB binary is **not vendored** in this repo — build it with
[`build-grub.sh`](build-grub.sh):

```sh
contrib/grub/build-grub.sh                 # -> contrib/grub/BOOTX64.EFI
nano11-go ... -iso-dir <tree> -iso-out out.iso -grub-efi contrib/grub/BOOTX64.EFI
```

nano11-go wraps that EFI application in a small FAT boot image (built in Go, see
`fatimg.go`) and points the ISO's UEFI El Torito entry at it. The BIOS boot
entry is left as Windows' native `etfsboot.com`.

## Why a patched GRUB

It has to be [a1ive's GRUB fork](https://github.com/a1ive/grub), not stock GRUB.
Only a1ive's `map` module provides a `wimboot` command that reads `boot.wim`
through GRUB's own filesystem drivers. Stock GRUB **cannot** launch the Windows
installer from optical media by any route:

- chainloading `bootmgr`/`cdboot` from the disc: they load and start, then exit
  — they need the El Torito/`cdboot` environment firmware normally provides;
- chainloading upstream `wimboot`: it runs but `OpenProtocol(DeviceHandle,
  SimpleFileSystem)` returns `EFI_UNSUPPORTED` — GRUB hands it a BlockIO handle
  with no filesystem protocol (true for iso9660 *and* UDF).

Stock GRUB only launches such a payload from a real FAT volume, i.e. USB/disk
boot — not an optical ISO. a1ive's `wimboot` sidesteps this by not using EFI's
SimpleFileSystem at all.

## Requirements / caveats

- **UEFI only**, and **Secure Boot must be OFF** — this GRUB is unsigned. (An
  `i386-pc` build would be needed to also front BIOS boot; not built here.)
- Building GRUB needs `bison` and `flex` in addition to gcc/autotools.
- Edit [`grub.cfg`](grub.cfg) to tune the menu (e.g. add bootloader paths to the
  autoprobe list); the config is embedded into the EFI at build time, so
  re-run `build-grub.sh` after changing it.
