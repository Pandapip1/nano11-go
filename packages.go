package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Pandapip1/gowim/component"
	"github.com/Pandapip1/gowim/wim"
)

// packagePatterns is a direct translation of nano11builder.ps1's
// $packagePatterns list (the DISM /Remove-Package pass): each entry is
// matched as a prefix against a servicing package's full identity string
// (Name~PublicKeyToken~Architecture~Language~Version, i.e. a
// servicing\Packages\*.mum file's own base name) exactly like the
// PowerShell `$_ -like "$packagePattern*"` check against `dism
// /Get-Packages`'s output. "$languageCode" entries are filled in from the
// image's own default UI language (see main.go) rather than assumed.
//
// Per the original script's own section comments, several of these are
// explicitly acknowledged there as aggressive/breakage-causing (Windows
// Defender, Search, Hello, BitLocker, TPM-WMI, LA57) -- ported as-is, not
// second-guessed, since reproducing nano11's actual behavior (including
// the parts that break things) is the point of this port.
func packagePatterns(languageCode string) []string {
	return []string{
		// --- Legacy Components & Optional Apps ---
		"Microsoft-Windows-InternetExplorer-Optional-Package~*",
		"Microsoft-Windows-MediaPlayer-Package~*",
		"Microsoft-Windows-WordPad-FoD-Package~*",
		"Microsoft-Windows-StepsRecorder-Package~*",
		"Microsoft-Windows-MSPaint-FoD-Package~*",
		"Microsoft-Windows-SnippingTool-FoD-Package~*",
		"Microsoft-Windows-TabletPCMath-Package~*",
		"Microsoft-Windows-Xps-Xps-Viewer-Opt-Package~*",
		"Microsoft-Windows-PowerShell-ISE-FOD-Package~*",
		"OpenSSH-Client-Package~*",

		// --- Language & Input Features (primary language only) ---
		"Microsoft-Windows-LanguageFeatures-Handwriting-" + languageCode + "-Package~*",
		"Microsoft-Windows-LanguageFeatures-OCR-" + languageCode + "-Package~*",
		"Microsoft-Windows-LanguageFeatures-Speech-" + languageCode + "-Package~*",
		"Microsoft-Windows-LanguageFeatures-TextToSpeech-" + languageCode + "-Package~*",
		"*IME-ja-jp*",
		"*IME-ko-kr*",
		"*IME-zh-cn*",
		"*IME-zh-tw*",

		// --- Core OS Features (removal is aggressive and will break functionality) ---
		"Windows-Defender-Client-Package~*",
		"Microsoft-Windows-Search-Engine-Client-Package~*",
		"Microsoft-Windows-Kernel-LA57-FoD-Package~*",

		// --- Security & Identity (breaks these features) ---
		"Microsoft-Windows-Hello-Face-Package~*",
		"Microsoft-Windows-Hello-BioEnrollment-Package~*",
		"Microsoft-Windows-BitLocker-DriveEncryption-FVE-Package~*",
		"Microsoft-Windows-TPM-WMI-Provider-Package~*",

		// --- Accessibility Tools ---
		"Microsoft-Windows-Narrator-App-Package~*",
		"Microsoft-Windows-Magnifier-App-Package~*",

		// --- Miscellaneous Features ---
		"Microsoft-Windows-Printing-PMCPPC-FoD-Package~*",
		"Microsoft-Windows-WebcamExperience-Package~*",
		"Microsoft-Media-MPEG2-Decoder-Package~*",
		"Microsoft-Windows-Wallpaper-Content-Extended-FoD-Package~*",
	}
}

// removeBloatPackages parses every servicing\Packages\*.mum file (skipping
// WinSxS\Manifests entirely -- deliberately not component.BuildFromImage,
// since the PA30 decode it requires is unneeded here and expensive; DISM's
// /Remove-Package operates on package-level identities only) and deletes
// each one matching packagePatterns via component.Remove.
func removeBloatPackages(r *wim.Reader, bt *wim.BlobTable, root *wim.DirEntry, languageCode string) error {
	children, err := root.ReadDir(component.PackagesDir)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read %s: %w", component.PackagesDir, err)
	}

	patterns := packagePatterns(languageCode)

	for _, c := range children {
		if c.IsDirectory() || !strings.HasSuffix(strings.ToLower(c.NameUTF8()), ".mum") {
			continue
		}
		name := c.NameUTF8()
		matched := false
		for _, pat := range patterns {
			if wim.MatchName(pat, name) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		data, err := r.ReadFile(root, bt, component.PackagesDir+`\`+name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		entry := component.ParseMUM(name, data)
		fmt.Printf("removing package: %s\n", name)
		if err := component.Remove(root, bt, entry); err != nil {
			return fmt.Errorf("remove package %s: %w", name, err)
		}
	}
	return nil
}
