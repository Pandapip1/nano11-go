#!/usr/bin/env bash
# Rebuilds a bootable Windows ISO around a gowim-modified install.wim.
#
# This is currently a plain shell script rather than Go code because
# gowim does not implement ISO writing *yet* -- not because it never
# will. gowim's TODO.md carries an open "ISO image creation subsystem"
# section (an ISO 9660 + Joliet/UDF writer and El Torito boot catalog
# support, matching what `oscdimg -udfver102` produces), confirmed still
# in scope on 2026-08-19. An earlier version of this comment claimed that
# section had been ruled out of scope; that was never true of gowim's
# TODO, and this script is expected to be replaced by the library's own
# writer once that lands.
#
# Everything gowim-specific (reading a real install.wim,
# removing AppX packages, disabling services, writing a new install.wim)
# happens in main.go; this script only re-assembles the surrounding ISO
# using genisoimage's UDF+El Torito support, exactly like community
# Windows-ISO-remaster recipes on Linux (oscdimg has no Linux
# equivalent).
#
# Usage: rebuild-iso.sh <extracted-iso-dir> <debloated-install.wim> <output.iso> <volid> [autounattend.xml]
set -euo pipefail

EXTRACT_DIR=$1
DEBLOATED_WIM=$2
OUT_ISO=$3
VOLID=$4
AUTOUNATTEND=${5:-}

cp "$DEBLOATED_WIM" "$EXTRACT_DIR/sources/install.wim"
rm -f "$EXTRACT_DIR/__chunk_data" # 7z-on-ISO extraction artifact, not part of the real image

# Final cleanup of the extracted media root (nano11builder.ps1's own
# last step before calling oscdimg): drop everything at the ISO root
# except what Setup actually needs to boot, matching the PS1's own
# hardcoded $keepList exactly. On a real retail ISO this discards
# autorun.inf, a stray root-level bootmgfw.efi (the real one lives under
# efi/microsoft/boot/, not the root), and the support/ folder.
KEEP_LIST=(boot efi sources bootmgr bootmgr.efi setup.exe autounattend.xml)
for entry in "$EXTRACT_DIR"/*; do
	name=$(basename "$entry")
	keep=false
	for k in "${KEEP_LIST[@]}"; do
		if [ "$name" = "$k" ]; then
			keep=true
			break
		fi
	done
	if ! $keep; then
		echo "Removing non-essential file/folder from ISO root: $name"
		rm -rf "$entry"
	fi
done

if [ -n "$AUTOUNATTEND" ]; then
	# nano11 keeps its own autounattend.xml at the ISO root (see its final
	# "keepList" cleanup pass) for unattended boot-time Setup, separate from
	# the copy main.go places at Windows\System32\Sysprep\autounattend.xml
	# inside the image itself.
	cp "$AUTOUNATTEND" "$EXTRACT_DIR/autounattend.xml"
fi

# efisys_noprompt.bin is Microsoft's own alternate UEFI El Torito boot image
# (ships alongside efisys.bin in every real Windows ISO, wrapping
# cdboot_noprompt.efi instead of cdboot.efi) -- used here instead of
# efisys.bin so Setup boots straight in, skipping the "Press any key to
# boot from CD or DVD..." prompt and its 5-second timing window entirely.
# There is no equivalent no-prompt variant of the BIOS-mode boot sector
# (boot/etfsboot.com), but that entry is only relevant to legacy BIOS boot;
# real UEFI firmware (and this project's qemu/OVMF test VMs) always boots
# via the EFI entry below.
genisoimage \
	-iso-level 4 -udf -allow-limited-size \
	-V "$VOLID" \
	-b boot/etfsboot.com -no-emul-boot -boot-load-size 8 -boot-info-table \
	-eltorito-alt-boot \
	-e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot \
	-o "$OUT_ISO" \
	"$EXTRACT_DIR"

echo "Wrote $OUT_ISO"
