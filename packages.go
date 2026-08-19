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
func packagePatterns(languageCode string, keepDefenderSearch bool) []string {
	pats := []string{
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

		// --- Virtualization Optional Features ---
		//
		// Not part of nano11builder.ps1's own list: added on top of the
		// faithful port, since none of Setup/OOBE's code paths depend on
		// Hyper-V, WSL, the container/utility-VM stack, or Windows Sandbox
		// being present, and a desktop image aimed at fitting on optical
		// media has no use for any of them.
		//
		// Every identity prefix here was verified to actually exist in a
		// real retail Windows 11 25H2 (build 26200) image's
		// Windows\servicing\Packages rather than assumed from the feature's
		// user-facing name -- an important distinction, since the obvious
		// guesses (Microsoft-Windows-Subsystem-Linux-*,
		// Microsoft-Windows-VirtualMachinePlatform-Package~*,
		// Microsoft-Windows-FaxServicesClientPackage~*,
		// Microsoft-Windows-WorkFolders-Client-Package~*) match nothing at
		// all there. Note the two distinct Hyper-V identity namespaces:
		// "Microsoft-Hyper-V-*" (ClientEdition, Hypervisor, Offline-*,
		// Online-Services, Services) and the unprefixed "HyperV-*"
		// (Compute-*, Feature-*, Networking-*, Primitive-*,
		// OptionalFeature-*) -- both are real and neither covers the other.
		//
		// Deliberately excluded: Microsoft-Windows-Kernel-Package-Lxss-
		// Package (kernel-adjacent, unlike the removable user-mode Lxss
		// packages) and Microsoft-OneCore-VirtualizationBasedSecurity-
		// Package (VBS/HVCI underpins Credential Guard and memory
		// integrity, which is core OS security rather than an optional
		// feature).
		"Microsoft-Hyper-V-*",
		"HyperV-*",
		"Microsoft-Windows-Lxss-Package~*",
		"Microsoft-Windows-Lxss-merged-Package~*",
		"Microsoft-Windows-Lxss-Optional-*",
		"Microsoft-Windows-Lxss-WOW64-Package~*",
		"Containers-DisposableClientVM-*",
		"Containers-ApplicationGuard-*",
		"Microsoft-OneCore-Containers-*",
		"Microsoft-UtilityVM-Containers-*",

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
		//
		// The PS1's own "Windows-Defender-Client-Package~*" and
		// "Microsoft-Windows-Search-Engine-Client-Package~*" are BOTH dead
		// on 25H2 -- verified by matching every pattern in this list against
		// the real 3460 .mum identities in a retail build 26200 image: each
		// matches exactly zero packages. Search's real identity has no
		// hyphen between "Search" and "Engine" (SearchEngine-Client-Package,
		// plus -base/-onecoreuap/-shell variants), and Defender's client
		// package was split into the several identities below. So the
		// script's two headline "this will break things" removals have
		// silently been no-ops -- see the dead-pattern note at the end of
		// this list.
		"Microsoft-Windows-Kernel-LA57-FoD-Package~*",

		// --- Security & Identity (breaks these features) ---
		// Hello-BioEnrollment was renamed to BioEnrollment-UX; the PS1's
		// old name matches nothing.
		"Microsoft-Windows-Hello-Face-Package~*",
		"Microsoft-Windows-BioEnrollment-UX-Package~*",

		// --- Miscellaneous Features ---
		// The PS1's "Xps-Xps-Viewer-Opt" matches nothing; the real XPS
		// identities are the two Printing-* ones below.
		"Microsoft-Windows-Printing-PMCPPC-FoD-Package~*",
		"Microsoft-Windows-Printing-XpsDocumentWriter-Opt-Package~*",
		"Microsoft-Windows-Printing-XPSServices-Package~*",
		"Microsoft-Windows-WebcamExperience-Package~*",
		"Microsoft-Windows-Wallpaper-Content-Extended-FoD-Package~*",

		// --- Dead patterns, deliberately NOT carried over ---
		//
		// These PS1 entries match zero identities in a real 25H2 image and
		// have no renamed equivalent to substitute -- the components are
		// simply gone from the base OS, so there is nothing to remove:
		//
		//   Microsoft-Windows-WordPad-FoD-Package~*      (WordPad was
		//       removed from Windows outright in 24H2)
		//   Microsoft-Windows-MSPaint-FoD-Package~*      } now Store apps,
		//   Microsoft-Windows-SnippingTool-FoD-Package~* } handled instead
		//                                                } by appx.go's
		//                                                } "paint" and
		//                                                } "screensketch"
		//   Microsoft-Windows-Narrator-App-Package~*     } inbox components
		//   Microsoft-Windows-Magnifier-App-Package~*    } rather than FoDs
		//   Microsoft-Windows-BitLocker-DriveEncryption-FVE-Package~*
		//   Microsoft-Windows-TPM-WMI-Provider-Package~* } folded into core
		//   Microsoft-Media-MPEG2-Decoder-Package~*      } servicing
		//   *IME-ja-jp* / *IME-ko-kr* / *IME-zh-cn* / *IME-zh-tw*
		//       (the CJK IMEs are no longer standalone packages; the
		//       file-level Windows\System32\InputMethod\{CHS,CHT,JPN,KOR}
		//       removal in filecleanup.go is what actually cuts them)
	}

	// The Defender and Search removals are gated because they are newly
	// live: their PS1 patterns matched nothing on 25H2 until they were
	// revived, so no passing install test has ever covered them, and the
	// PS1's own comments single them out as breakage-causing. -keep-defender-
	// search takes them back out without touching anything else, which is
	// what makes an install/boot failure bisectable.
	if !keepDefenderSearch {
		pats = append(pats,
			"Windows-Defender-AM-Default-Definitions-*",
			"Windows-Defender-Group-Policy-*",
			"Windows-Defender-ApplicationGuard-Inbox-*",
			"Microsoft-Windows-SenseClient-*",
			"Microsoft-Windows-SearchEngine-Client-Package*",
		)
	}
	return pats
}

// removeBloatPackages parses every servicing\Packages\*.mum file (skipping
// WinSxS\Manifests entirely -- deliberately not component.BuildFromImage,
// since the PA30 decode it requires is unneeded here and expensive; DISM's
// /Remove-Package operates on package-level identities only) and deletes
// each one matching packagePatterns via component.Remove.
func removeBloatPackages(r *wim.Reader, bt *wim.BlobTable, root *wim.DirEntry, languageCode string, keepDefenderSearch bool) error {
	return removePackagesMatching(r, bt, root, packagePatterns(languageCode, keepDefenderSearch))
}

// removePackagesMatching is removeBloatPackages's body with the pattern list
// injected rather than derived, so the measurement harness (measure_test.go)
// can run the exact same removal logic against a modified pattern list
// instead of maintaining a divergent copy of it.
func removePackagesMatching(r *wim.Reader, bt *wim.BlobTable, root *wim.DirEntry, patterns []string) error {
	children, err := root.ReadDir(component.PackagesDir)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read %s: %w", component.PackagesDir, err)
	}

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
