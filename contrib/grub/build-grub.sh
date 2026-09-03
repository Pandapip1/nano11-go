#!/usr/bin/env bash
#
# Build the GRUB artifacts nano11-go's -grub-efi and -grub-bios-hybrid flags
# consume: a UEFI EFI application (unchanged from before) plus, now, the
# legacy-BIOS pieces needed to make the same GRUB menu (or, for the default
# non-GRUB ISO, a silent straight-to-Setup boot) also reachable when the ISO
# is written byte-for-byte to a USB stick and booted in BIOS/CSM mode.
#
# The binaries are deliberately NOT vendored into this repo: GRUB is GPLv3 and
# large, and a prebuilt bootloader is exactly the kind of artifact you want to
# be able to reproduce from source. This script does that.
#
# It must be a1ive's GRUB fork, not stock GRUB: only a1ive's `wimboot` module
# (grub-core/map/wimboot/, a module distinct from `map` itself -- easy to
# miss, and this script did for a while -- see Makefile.core.def's own
# `wimboot` module stanza) provides a `wimboot` command that reads boot.wim
# through GRUB's own filesystem layer, confirmed to also build for i386-pc
# (that same stanza lists `i386_pc = map/wimboot/pc/boot.c;` and
# `enable = i386_pc;`), so the same mechanism works in BIOS mode too. Stock
# GRUB cannot launch the Windows installer from an optical/iso9660 volume at
# all (its chainloader hands the payload a BlockIO handle with no
# SimpleFileSystem, and bootmgr/wimboot both need one). See the repo's memory
# note grub-wimboot-optical-boot for the UEFI side of that investigation.
#
# Why two separate GRUB builds (x86_64-efi and i386-pc): GRUB's autotools
# build is configured for exactly one target platform at a time (./configure
# --target=... --with-platform=...); a single source checkout cannot be
# reconfigured for a second platform without a full distclean. So this script
# clones/builds into two sibling directories under WORKDIR.
#
# Why i386-pc-eltorito, not a hand-rolled concatenation: `-O i386-pc-eltorito`
# is the exact format grub-mkrescue itself uses for its BIOS El Torito image
# (util/grub-mkrescue.c's "build BIOS core.img" section passes literally
# "i386-pc-eltorito" to grub_install_make_image_wrap). Per its implementation
# (util/mkimage.c, case IMAGE_I386_PC_ELTORITO), it writes out cdboot.img (a
# fixed 2048 bytes -- El Torito's own CD-emulation loader, analogous to
# Windows' etfsboot.com) immediately followed by diskboot.img (with its
# embedded blocklist length patched to match the compressed kernel+modules
# that follow) and the kernel+modules themselves. That whole blob is
# registered as the El Torito BIOS ("no emulation") boot entry.
#
# Why grub.cfg is embedded via a memdisk tar (-m), not grub-mkimage's -c:
# `-c FILE` embeds FILE as a one-shot early-boot script (grub_parser_execute
# in grub-core/kern/main.c) -- menuentry commands in it register menu
# entries, but nothing then *renders* them, so boot fell straight through to
# a bare `grub>` shell (confirmed empirically under QEMU/SeaBIOS: the menu
# never appeared). A memdisk instead puts grub.cfg at an actual file path
# (`(memdisk)/boot/grub/grub.cfg`) that `normal`'s own config lookup already
# searches (prefix auto-set to `(memdisk)/boot/grub` by -m) -- kern/main.c
# calls `grub_load_normal_mode` unconditionally after the early script runs,
# which is what actually shows the menu once it can find that file. This is
# also exactly what the UEFI build's grub-mkstandalone does under the hood
# for its own `path=file` graft points, just spelled out by hand here since
# grub-mkimage (not grub-mkstandalone) is what -O i386-pc-eltorito's stricter
# 480 KiB core-image ceiling can actually fit (util/mkimage.c: `core_size +
# GRUB_KERNEL_I386_PC_LINK_ADDR > 0x78000` fails the build, which grub-mkimage
# hits with e.g. all_video, hence the i386-pc module list below being trimmed
# to what fits alongside the menu/wimboot/memdisk-reading modules it needs).
#
# Why boot_hybrid.img, not boot.img: boot.img (grub-core/boot/i386/pc/
# boot.S, the plain non-HYBRID_BOOT build) keeps its `kernel_sector` field
# inside the BIOS Parameter Block area (offset 0x5C) and is meant for
# grub-bios-setup to install onto a live, already-partitioned disk (it
# copies that disk's real BPB back in first). boot_hybrid.img is built with
# -DHYBRID_BOOT (grub-core/Makefile.core.def's `boot_hybrid` image stanza),
# which relocates that field to offset 0x1B0-0x1B7 instead, out of the way of
# a real MBR partition table at 0x1BE -- exactly what an isohybrid MBR needs,
# and exactly the file real xorriso's own `--grub2-mbr` flag takes (see
# libisofs/system_area.c's "Patch MBR for GRUB2" code, which patches this
# same 0x1B0 field to point at wherever the El Torito BIOS image landed).
# nano11-go/gowim replicate that same patch in pure Go (see gowim's
# iso.Options.LegacyBIOS / iso/hybridmbr.go) rather than shelling out to
# xorriso, so this script only needs to hand over the raw template.
#
# Usage:
#   contrib/grub/build-grub.sh [WORKDIR] [OUTDIR]
#     WORKDIR  scratch dir for the GRUB source/build (default: /tmp/nano11-grub-build)
#     OUTDIR   where to write the built artifacts    (default: this script's own directory)
#
# Produces, in OUTDIR:
#   BOOTX64.EFI              UEFI application (x86_64-efi), unchanged from before
#   BOOT_HYBRID.img          512-byte isohybrid MBR template (i386-pc)
#   BIOS_ELTORITO_MENU.img   BIOS El Torito image running grub.cfg's menu
#   BIOS_ELTORITO_SILENT.img BIOS El Torito image silently wimboot-ing straight to Setup
#
# Then:
#   nano11-go ... -iso-dir <tree> -iso-out out.iso \
#     -grub-efi contrib/grub/BOOTX64.EFI \
#     -grub-bios-hybrid contrib/grub/BOOT_HYBRID.img \
#     -grub-bios-eltorito contrib/grub/BIOS_ELTORITO_MENU.img   # or _SILENT.img
#
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${1:-/tmp/nano11-grub-build}"
OUTDIR="${2:-$HERE}"
GRUB_REPO="https://github.com/a1ive/grub"
CFG_MENU="$HERE/grub.cfg"
CFG_SILENT="$HERE/grub-silent.cfg"

# GRUB's build needs bison and flex in addition to the usual autotools/gcc.
missing=()
for t in gcc make bison flex autoconf automake python3 pkg-config gettext git tar; do
	command -v "$t" >/dev/null 2>&1 || missing+=("$t")
done
if [ "${#missing[@]}" -ne 0 ]; then
	echo "error: missing build tools: ${missing[*]}" >&2
	echo "install them (e.g. apt-get install ${missing[*]}) and re-run." >&2
	exit 1
fi

mkdir -p "$WORK" "$OUTDIR"

# clone_and_bootstrap SRC_DIR: clones (or updates) a1ive's GRUB into SRC_DIR
# and runs ./bootstrap, leaving ./configure ready to run.
clone_and_bootstrap() {
	local src="$1"
	if [ -d "$src/.git" ]; then
		echo ">> updating existing GRUB checkout in $src"
		git -C "$src" fetch --depth 1 origin
		git -C "$src" reset --hard FETCH_HEAD
	else
		echo ">> cloning a1ive GRUB into $src"
		git clone --depth 1 "$GRUB_REPO" "$src"
	fi

	(
		cd "$src"
		# bootstrap fetches gnulib; on a fresh clone it sometimes stops after
		# the gnulib clone without generating ./configure, so run it again
		# pointed at the now-local gnulib if configure is still missing.
		if [ ! -x ./configure ]; then
			echo ">> bootstrap (fetch gnulib)"
			./bootstrap || true
		fi
		if [ ! -x ./configure ]; then
			echo ">> bootstrap again with local gnulib"
			./bootstrap --no-git --gnulib-srcdir=./gnulib
		fi
	)
}

### x86_64-efi build (UEFI, unchanged from before) ###

EFI_SRC="$WORK/grub-efi"
clone_and_bootstrap "$EFI_SRC"
(
	cd "$EFI_SRC"
	echo ">> configure (x86_64-efi)"
	./configure --target=x86_64 --with-platform=efi --disable-werror >/dev/null

	echo ">> make (x86_64-efi)"
	make -j"$(nproc)" >/dev/null

	# fwsetup lives in reboot.mod in this fork (there is no efifwsetup.mod).
	echo ">> grub-mkstandalone -> $OUTDIR/BOOTX64.EFI"
	./grub-mkstandalone -O x86_64-efi --directory=grub-core \
		--modules="map wimboot iso9660 udf part_gpt part_msdos fat chain normal search search_fs_file configfile echo test sleep all_video reboot linux" \
		-o "$OUTDIR/BOOTX64.EFI" \
		"boot/grub/grub.cfg=$CFG_MENU"
)

### i386-pc build (legacy BIOS) ###

# grub-mkimage's -m/--memdisk takes a raw tar, not the path=file graft-point
# syntax grub-mkstandalone's -c accepts (there is no grub-mkstandalone-alike
# convenience for i386-pc-eltorito's stricter size budget), so build the two
# tiny ustar archives by hand: each holds exactly one file, boot/grub/grub.cfg.
MENU_TAR_DIR="$WORK/menu-tar-src"
SILENT_TAR_DIR="$WORK/silent-tar-src"
rm -rf "$MENU_TAR_DIR" "$SILENT_TAR_DIR"
mkdir -p "$MENU_TAR_DIR/boot/grub" "$SILENT_TAR_DIR/boot/grub"
cp "$CFG_MENU" "$MENU_TAR_DIR/boot/grub/grub.cfg"
cp "$CFG_SILENT" "$SILENT_TAR_DIR/boot/grub/grub.cfg"
MENU_TAR="$WORK/menu.tar"
SILENT_TAR="$WORK/silent.tar"
tar --format=ustar -cf "$MENU_TAR" -C "$MENU_TAR_DIR" boot
tar --format=ustar -cf "$SILENT_TAR" -C "$SILENT_TAR_DIR" boot

BIOS_SRC="$WORK/grub-bios"
clone_and_bootstrap "$BIOS_SRC"
(
	cd "$BIOS_SRC"
	echo ">> configure (i386-pc)"
	./configure --target=i386 --with-platform=pc --disable-werror >/dev/null

	echo ">> make (i386-pc)"
	make -j"$(nproc)" >/dev/null

	echo ">> boot_hybrid.img -> $OUTDIR/BOOT_HYBRID.img"
	cp grub-core/boot_hybrid.img "$OUTDIR/BOOT_HYBRID.img"

	# Same module list as the UEFI build, minus all_video/linux (no benefit
	# here, and all_video alone is what pushes a memdisk build over -O
	# i386-pc-eltorito's 480 KiB core-image ceiling) plus biosdisk (BIOS
	# INT13h disk access -- UEFI needs no equivalent, firmware already
	# provides disk I/O through its own protocols) and memdisk/tar (to embed
	# grub.cfg as an actual file rather than a one-shot script; see the file
	# comment).
	MODULES="map wimboot iso9660 udf part_gpt part_msdos fat chain normal search search_fs_file configfile echo test sleep reboot biosdisk memdisk tar"

	echo ">> grub-mkimage (i386-pc-eltorito, menu) -> $OUTDIR/BIOS_ELTORITO_MENU.img"
	./grub-mkimage -O i386-pc-eltorito --directory=grub-core \
		-m "$MENU_TAR" \
		-o "$OUTDIR/BIOS_ELTORITO_MENU.img" \
		$MODULES

	echo ">> grub-mkimage (i386-pc-eltorito, silent) -> $OUTDIR/BIOS_ELTORITO_SILENT.img"
	./grub-mkimage -O i386-pc-eltorito --directory=grub-core \
		-m "$SILENT_TAR" \
		-o "$OUTDIR/BIOS_ELTORITO_SILENT.img" \
		$MODULES
)

echo ">> done:"
ls -la "$OUTDIR/BOOTX64.EFI" "$OUTDIR/BOOT_HYBRID.img" "$OUTDIR/BIOS_ELTORITO_MENU.img" "$OUTDIR/BIOS_ELTORITO_SILENT.img"
