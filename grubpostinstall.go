package nano11go

import (
	"fmt"

	"github.com/Pandapip1/gowim/wim"
)

// grubPostInstallDir is where the GRUB EFI application and the scripts that
// install it are staged inside the image -- Windows\Setup\Scripts already
// exists as a Setup-owned location (SetupComplete.cmd's documented home:
// Microsoft Learn "Add a Custom Script to Windows Setup"), and Setup runs
// SetupComplete.cmd from there, as SYSTEM, after it finishes writing the
// ESP/BCD boot configuration but before the logon screen ever appears --
// exactly the window this needs: late enough that Setup's own bootmgfw.efi/
// BCD write has already happened, early enough that it runs unconditionally
// on every install with no user or reboot in between.
const grubPostInstallDir = `Windows\Setup\Scripts`

// grubEFIName is the filename the GRUB EFI application is staged under
// inside the image and, after install-grub.ps1 copies it, on the ESP
// itself.
const grubEFIName = "grubx64.efi"

// setupCompleteCmd invokes install-grub.ps1 with the standard
// bypass/unrestricted execution policy flags (SetupComplete.cmd itself has
// no policy of its own to fight; PowerShell's default policy would
// otherwise block an unsigned local script), logging its output for
// post-mortem debugging since SetupComplete.cmd runs with no console
// visible to a human.
const setupCompleteCmd = `@echo off
powershell -NoProfile -ExecutionPolicy Bypass -File "%WINDIR%\Setup\Scripts\install-grub.ps1" >> "%WINDIR%\Setup\Scripts\install-grub.log" 2>&1
`

// installGRUBPS1Template is run by SetupComplete.cmd, as SYSTEM, after
// Setup has already written its own ESP boot files
// (\EFI\Microsoft\Boot\bootmgfw.efi + BCD) and registered the firmware
// NVRAM entry for {bootmgr}, but before any reboot. It repoints that same
// {bootmgr} NVRAM/BCD object at the GRUB EFI application staged alongside
// this script (rather than creating a second, independent NVRAM entry --
// see BCDEdit /set and "BCD System Store Settings for UEFI", Microsoft
// Learn), after moving Setup's own bootmgfw.efi out of the way to the path
// GRUB's embedded config (grub.cfg's `chainloader
// /EFI/Microsoft/Boot/bootmgfw-orig.efi`) expects to chainload.
//
// GRUB itself never touches BCD -- it has no BCD-parsing module (confirmed:
// GNU GRUB manual's "Chain-loading" documents chainloader/ntfs/chain as the
// only Windows-boot mechanism, with no bcd-aware alternative) -- so once
// bcdedit repoints {bootmgr}, GRUB's chainloader command loads the
// unmodified, Setup-written bootmgfw.efi at its new name and hands off to
// it exactly as firmware would have done directly; BCD's own contents are
// never edited, only the firmware boot manager's target for the well-known
// {bootmgr} object.
//
// %s is grubEFIName, substituted once at build time (fmt.Sprintf, not a
// runtime template) -- there is exactly one caller and no per-install
// variation, so this stays a plain constant-shaped string rather than
// pulling in text/template for one substitution.
const installGRUBPS1Template = `$ErrorActionPreference = 'Stop'
$esp = Get-Partition | Where-Object { $_.GptType -eq '{c12a7328-f81f-11d2-ba4b-00a0c93ec93b}' } | Select-Object -First 1
if (-not $esp) { throw "no EFI System Partition found" }
$vol = $esp | Get-Volume
$letter = $vol.DriveLetter
if (-not $letter) {
    $letter = 'S'
    Add-PartitionAccessPath -DiskNumber $esp.DiskNumber -PartitionNumber $esp.PartitionNumber -AccessPath "${letter}:\"
}
$espRoot = "${letter}:\"
New-Item -ItemType Directory -Force -Path ($espRoot + 'EFI\GRUB') | Out-Null
Copy-Item -Force ($env:WINDIR + '\Setup\Scripts\%[1]s') ($espRoot + 'EFI\GRUB\%[1]s')
$bootmgfw = $espRoot + 'EFI\Microsoft\Boot\bootmgfw.efi'
$bootmgfwOrig = $espRoot + 'EFI\Microsoft\Boot\bootmgfw-orig.efi'
if ((Test-Path $bootmgfw) -and -not (Test-Path $bootmgfwOrig)) {
    Rename-Item $bootmgfw 'bootmgfw-orig.efi'
}
& bcdedit /set '{bootmgr}' path ('\EFI\GRUB\%[1]s')
Write-Output "GRUB installed as {bootmgr} target."
`

// installGRUBPostInstall stages grubEFI (a prebuilt GRUB x86_64-efi
// standalone application -- see cmd/nano11-go's -grub-postinstall flag and
// the module list required, documented there) plus SetupComplete.cmd and
// install-grub.ps1 into the image at grubPostInstallDir, so that on first
// boot after Setup completes, GRUB -- not bootmgfw.efi directly -- is what
// the firmware loads by default, and GRUB in turn chainloads Setup's own
// (renamed) bootmgfw.efi to actually start Windows.
//
// This only arranges for GRUB to become the *default* boot target; it does
// not remove or disable Windows' own boot manager/BCD in any way stronger
// than the {bootmgr} path repoint described above, so the change is a
// single bcdedit call away from being reverted by hand if needed.
func installGRUBPostInstall(root *wim.DirEntry, bt *wim.BlobTable, newBlobs map[wim.Hash][]byte, grubEFI []byte) error {
	fmt.Println("Installing post-install GRUB bootloader files...")
	if err := writeFile(root, bt, newBlobs, grubPostInstallDir+`\`+grubEFIName, grubEFI); err != nil {
		return fmt.Errorf("write %s: %w", grubEFIName, err)
	}
	ps1 := fmt.Sprintf(installGRUBPS1Template, grubEFIName)
	if err := writeFile(root, bt, newBlobs, grubPostInstallDir+`\install-grub.ps1`, []byte(ps1)); err != nil {
		return fmt.Errorf("write install-grub.ps1: %w", err)
	}
	if err := writeFile(root, bt, newBlobs, grubPostInstallDir+`\SetupComplete.cmd`, []byte(setupCompleteCmd)); err != nil {
		return fmt.Errorf("write SetupComplete.cmd: %w", err)
	}
	return nil
}
