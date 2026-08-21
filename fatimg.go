package main

import (
	"encoding/binary"
	"fmt"
)

// FAT16 boot-image authoring for the -grub-efi option.
//
// gowim's iso.BootEntry names a file *inside* the ISO tree as the El Torito
// boot image, and for a UEFI "no emulation" entry that file must itself be a
// FAT filesystem the firmware mounts to run \EFI\BOOT\BOOTX64.EFI (this is how
// Windows' own efi/microsoft/boot/efisys.bin works, and how firmware loads
// GRUB). gowim has no FAT writer, and shelling out to mkfs.vfat/mtools would
// reintroduce the external-tool dependency this project deliberately removed
// (see isoimage.go). So the one tiny FAT image the GRUB option needs is built
// here in Go.
//
// The scope is deliberately minimal -- exactly one file at a fixed path,
// \EFI\BOOT\BOOTX64.EFI -- so there is no long-filename machinery: EFI, BOOT
// and BOOTX64.EFI are all valid 8.3 names. FAT16 is used (not FAT12/FAT32) to
// match what mkfs.vfat produces for a boot image of this size and what the
// hand-built probe images used; the cluster count is forced above the 4085
// FAT12/16 boundary so the type is unambiguous.

const (
	fatBytesPerSector    = 512
	fatSectorsPerCluster = 1 // 512-byte clusters
	fatRootEntries       = 512
	fatNumFATs           = 2
	fatReservedSectors   = 1
	// Force the cluster count above the FAT12/16 boundary (4085) so the
	// volume is unambiguously FAT16 even for a small BOOTX64.EFI.
	fatMinDataClusters = 4200
)

// dirEntry32 encodes one 32-byte FAT directory entry (8.3 only).
func dirEntry32(name8_3 string, attr byte, firstCluster uint16, size uint32) []byte {
	e := make([]byte, 32)
	copy(e[0:11], []byte(name8_3)) // caller passes the 11-byte space-padded field
	e[11] = attr
	binary.LittleEndian.PutUint16(e[26:28], firstCluster)
	binary.LittleEndian.PutUint32(e[28:32], size)
	return e
}

// name83 space-pads a name/ext into the 11-byte on-disk directory field.
func name83(name, ext string) string {
	n := name + "        "
	x := ext + "   "
	return n[:8] + x[:3]
}

// buildGrubFATImage returns a FAT16 filesystem image whose only content is
// \EFI\BOOT\BOOTX64.EFI == grub. It is suitable as a gowim iso UEFI El Torito
// boot image.
func buildGrubFATImage(grub []byte) ([]byte, error) {
	if len(grub) == 0 {
		return nil, fmt.Errorf("grub EFI image is empty")
	}

	clusterBytes := fatBytesPerSector * fatSectorsPerCluster

	ceilDiv := func(a, b int) int { return (a + b - 1) / b }

	fileClusters := ceilDiv(len(grub), clusterBytes)
	// Cluster 2 = \EFI dir, cluster 3 = \EFI\BOOT dir, clusters 4.. = file.
	dataClusters := 2 + fileClusters
	if dataClusters < fatMinDataClusters {
		dataClusters = fatMinDataClusters
	}
	if dataClusters > 65524 {
		return nil, fmt.Errorf("grub EFI image too large for FAT16 boot image (%d bytes)", len(grub))
	}

	fatEntries := dataClusters + 2 // clusters are numbered from 2
	fatSectors := ceilDiv(fatEntries*2, fatBytesPerSector)
	rootDirSectors := ceilDiv(fatRootEntries*32, fatBytesPerSector)
	dataSectors := dataClusters * fatSectorsPerCluster
	totalSectors := fatReservedSectors + fatNumFATs*fatSectors + rootDirSectors + dataSectors

	img := make([]byte, totalSectors*fatBytesPerSector)

	// --- Boot sector / BPB ---
	bs := img[0:fatBytesPerSector]
	bs[0], bs[1], bs[2] = 0xEB, 0x3C, 0x90 // JMP + NOP
	copy(bs[3:11], []byte("MSWIN4.1"))
	binary.LittleEndian.PutUint16(bs[11:13], fatBytesPerSector)
	bs[13] = fatSectorsPerCluster
	binary.LittleEndian.PutUint16(bs[14:16], fatReservedSectors)
	bs[16] = fatNumFATs
	binary.LittleEndian.PutUint16(bs[17:19], fatRootEntries)
	if totalSectors < 0x10000 {
		binary.LittleEndian.PutUint16(bs[19:21], uint16(totalSectors))
	} // else stays 0 and totSec32 below carries it
	bs[21] = 0xF8 // media descriptor (fixed disk)
	binary.LittleEndian.PutUint16(bs[22:24], uint16(fatSectors))
	binary.LittleEndian.PutUint16(bs[24:26], 32) // sectors per track (nominal)
	binary.LittleEndian.PutUint16(bs[26:28], 64) // heads (nominal)
	binary.LittleEndian.PutUint32(bs[28:32], 0)  // hidden sectors
	binary.LittleEndian.PutUint32(bs[32:36], uint32(totalSectors))
	// FAT16 extended boot record
	bs[36] = 0x80                                        // drive number
	bs[38] = 0x29                                        // extended boot signature
	binary.LittleEndian.PutUint32(bs[39:43], 0x4E414E4F) // volume id
	copy(bs[43:54], []byte("NANO11GRUB "))
	copy(bs[54:62], []byte("FAT16   "))
	bs[510], bs[511] = 0x55, 0xAA

	// --- FAT tables ---
	fat := make([]byte, fatSectors*fatBytesPerSector)
	putFAT := func(cluster int, value uint16) {
		binary.LittleEndian.PutUint16(fat[cluster*2:cluster*2+2], value)
	}
	putFAT(0, 0xFFF8) // media descriptor in FAT[0]
	putFAT(1, 0xFFFF) // EOC marker in FAT[1]
	putFAT(2, 0xFFFF) // \EFI directory: single cluster, EOC
	putFAT(3, 0xFFFF) // \EFI\BOOT directory: single cluster, EOC
	// File cluster chain: clusters 4 .. 4+fileClusters-1.
	fileFirst := 4
	for i := 0; i < fileClusters; i++ {
		c := fileFirst + i
		if i == fileClusters-1 {
			putFAT(c, 0xFFFF)
		} else {
			putFAT(c, uint16(c+1))
		}
	}

	fatStart := fatReservedSectors * fatBytesPerSector
	for i := 0; i < fatNumFATs; i++ {
		copy(img[fatStart+i*len(fat):], fat)
	}

	// --- Root directory: single entry "EFI" (dir) -> cluster 2 ---
	rootStart := (fatReservedSectors + fatNumFATs*fatSectors) * fatBytesPerSector
	copy(img[rootStart:], dirEntry32(name83("EFI", ""), 0x10, 2, 0))

	dataStart := rootStart + rootDirSectors*fatBytesPerSector
	clusterOffset := func(cluster int) int {
		return dataStart + (cluster-2)*clusterBytes
	}

	// --- \EFI directory (cluster 2): ".", "..", "BOOT" -> cluster 3 ---
	efiDir := img[clusterOffset(2):]
	copy(efiDir[0:32], dirEntry32(name83(".", ""), 0x10, 2, 0))
	copy(efiDir[32:64], dirEntry32(name83("..", ""), 0x10, 0, 0)) // ".." of a root child points to cluster 0
	copy(efiDir[64:96], dirEntry32(name83("BOOT", ""), 0x10, 3, 0))

	// --- \EFI\BOOT directory (cluster 3): ".", "..", "BOOTX64.EFI" ---
	bootDir := img[clusterOffset(3):]
	copy(bootDir[0:32], dirEntry32(name83(".", ""), 0x10, 3, 0))
	copy(bootDir[32:64], dirEntry32(name83("..", ""), 0x10, 2, 0)) // ".." -> parent \EFI (cluster 2)
	copy(bootDir[64:96], dirEntry32(name83("BOOTX64", "EFI"), 0x20, uint16(fileFirst), uint32(len(grub))))

	// --- File data ---
	copy(img[clusterOffset(fileFirst):], grub)

	return img, nil
}
