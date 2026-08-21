#!/usr/bin/env bash
#
# Build the GRUB EFI application that nano11-go's -grub-efi flag consumes.
#
# The binary is deliberately NOT vendored into this repo: GRUB is GPLv3 and
# large, and a prebuilt bootloader is exactly the kind of artifact you want to
# be able to reproduce from source. This script does that.
#
# It must be a1ive's GRUB fork, not stock GRUB: only a1ive's `map` module
# provides a `wimboot` command that reads boot.wim through GRUB's own
# filesystem layer. Stock GRUB cannot launch the Windows installer from an
# optical/iso9660 volume at all (its chainloader hands the payload a BlockIO
# handle with no SimpleFileSystem, and bootmgr/wimboot both need one). See the
# repo's memory note grub-wimboot-optical-boot for the full investigation.
#
# The resulting ISO boots UEFI only and requires Secure Boot to be OFF (this
# GRUB is unsigned).
#
# Usage:
#   contrib/grub/build-grub.sh [WORKDIR] [OUTFILE]
#     WORKDIR  scratch dir for the GRUB source/build (default: /tmp/nano11-grub-build)
#     OUTFILE  where to write the EFI app       (default: contrib/grub/BOOTX64.EFI)
#
# Then:
#   nano11-go ... -iso-dir <tree> -iso-out out.iso -grub-efi contrib/grub/BOOTX64.EFI
#
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${1:-/tmp/nano11-grub-build}"
OUT="${2:-$HERE/BOOTX64.EFI}"
GRUB_REPO="https://github.com/a1ive/grub"
CFG="$HERE/grub.cfg"

# GRUB's build needs bison and flex in addition to the usual autotools/gcc.
missing=()
for t in gcc make bison flex autoconf automake python3 pkg-config gettext git; do
	command -v "$t" >/dev/null 2>&1 || missing+=("$t")
done
if [ "${#missing[@]}" -ne 0 ]; then
	echo "error: missing build tools: ${missing[*]}" >&2
	echo "install them (e.g. apt-get install ${missing[*]}) and re-run." >&2
	exit 1
fi

mkdir -p "$WORK"
SRC="$WORK/grub"
if [ -d "$SRC/.git" ]; then
	echo ">> updating existing GRUB checkout in $SRC"
	git -C "$SRC" fetch --depth 1 origin
	git -C "$SRC" reset --hard FETCH_HEAD
else
	echo ">> cloning a1ive GRUB into $SRC"
	git clone --depth 1 "$GRUB_REPO" "$SRC"
fi

cd "$SRC"

# bootstrap fetches gnulib; on a fresh clone it sometimes stops after the
# gnulib clone without generating ./configure, so run it again pointed at the
# now-local gnulib if configure is still missing.
if [ ! -x ./configure ]; then
	echo ">> bootstrap (fetch gnulib)"
	./bootstrap || true
fi
if [ ! -x ./configure ]; then
	echo ">> bootstrap again with local gnulib"
	./bootstrap --no-git --gnulib-srcdir=./gnulib
fi

echo ">> configure (x86_64-efi)"
./configure --target=x86_64 --with-platform=efi --disable-werror >/dev/null

echo ">> make"
make -j"$(nproc)" >/dev/null

# fwsetup lives in reboot.mod in this fork (there is no efifwsetup.mod).
echo ">> grub-mkstandalone -> $OUT"
./grub-mkstandalone -O x86_64-efi --directory=grub-core \
	--modules="map iso9660 udf part_gpt part_msdos fat chain normal search search_fs_file configfile echo test sleep all_video reboot linux" \
	-o "$OUT" \
	"boot/grub/grub.cfg=$CFG"

echo ">> done: $OUT"
ls -la "$OUT"
