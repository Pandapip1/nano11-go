package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/Pandapip1/gowim/appx"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// bloatAppxKeywords is a direct, line-for-line translation of
// nano11builder.ps1's Get-AppxProvisionedPackage filter (each `-like
// '*X*'` becomes one entry here, matched case-insensitively against the
// substring of a provisioned package's family name before the publisher ID
// -- see isBloatFamily), not a curated/shortened subset.
var bloatAppxKeywords = []string{
	"zune", "bing", "clipchamp", "gaming", "people", "powerautomate",
	"teams", "todos", "yourphone", "soundrecorder", "solitaire",
	"feedbackhub", "maps", "officehub", "help", "family", "alarms",
	"communicationsapps", "copilot", "compatibilityenhancements",
	"av1videoextension", "avcencodervideoextension", "heifimageextension",
	"hevcvideoextension", "microsoftstickynotes", "outlookforwindows",
	"rawimageextension", "sechealthui", "vp9videoextensions",
	"webpimageextension", "devhome", "photos", "camera", "quickassist",
	"coreai", "peopleexperiencehost", "pinningconfirmationdialog",
	"secureassessmentbrowser", "paint", "notepad",

	// Not part of nano11builder.ps1's own keyword list -- added on top of
	// the faithful port since they're real provisioned families the
	// existing keywords don't happen to substring-match: Xbox identity/
	// overlay/speech packages (family names like "Microsoft.Xbox.TCUI",
	// "Microsoft.XboxIdentityProvider", "Microsoft.XboxSpeechToTextOverlay"
	// don't contain "gaming"), the Widgets board
	// ("MicrosoftWindows.Client.WebExperience"), and Snip & Sketch
	// ("Microsoft.ScreenSketch", distinct from the SnippingTool-FoD
	// servicing package already removed in packages.go).
	"xbox", "webexperience", "screensketch",
}

// storeAppxKeywords are the remaining Store-distributed UWP *apps* that survive
// the default bloat pass: the Microsoft Store itself and a handful of utilities
// and media/codec extensions. They are removed only under -remove-store-apps
// (stageFlags.removeStoreApps), not by default, because unlike the telemetry/
// consumer bloat above these are apps a user may legitimately want (winget,
// Terminal, Calculator). The runtime *frameworks* they and the shell depend on
// (WindowsAppRuntime, VCLibs, UI.Xaml, NET.Native) are deliberately NOT here --
// removing those is a separate, higher-risk step. All eight were verified
// present as provisioned packages in a real 25H2 (build 26200) image.
var storeAppxKeywords = []string{
	"windowsstore", "storepurchaseapp", "desktopappinstaller",
	"windowsterminal", "windowscalculator", "crossdevice",
	"mpeg2videoextension", "webmediaextensions",
}

// frameworkAppxKeywords are the UWP runtime *frameworks* the removed apps (and
// potentially the shell) depend on. Removing them only makes sense once the
// apps that need them are gone, so -remove-uwp-frameworks implies
// -remove-store-apps. This is the higher-risk tier: the Win11 shell's own
// framework copies live under Windows\SystemApps (the *.CBS variants), so in
// principle the WindowsApps copies here are only for user apps -- but that is
// exactly what the boot-to-desktop test has to confirm.
var frameworkAppxKeywords = []string{
	"windowsappruntime", "vclibs", "ui.xaml", "net.native",
}

// provisioningPath is the offline source of truth for provisioned AppX
// packages (see the sibling appx package's own doc comments).
const provisioningPath = `ProgramData\Microsoft\Windows\AppxProvisioning.xml`

func removeBloatAppx(r *wim.Reader, bt *wim.BlobTable, root *wim.DirEntry, software *registry.Hive, newBlobs map[wim.Hash][]byte, removeStoreApps, removeUWPFrameworks bool) error {
	data, err := r.ReadFile(root, bt, provisioningPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", provisioningPath, err)
	}
	pl, err := appx.ParseProvisioning(data)
	if err != nil {
		return err
	}

	keywords := append([]string{}, bloatAppxKeywords...)
	if removeStoreApps || removeUWPFrameworks {
		keywords = append(keywords, storeAppxKeywords...)
	}
	if removeUWPFrameworks {
		keywords = append(keywords, frameworkAppxKeywords...)
	}

	applications := software.Hive.Root.FindOrCreatePath(appx.ApplicationsPath)
	deprovisioned := software.Hive.Root.FindOrCreatePath(appx.DeprovisionedPath)

	families := map[string]bool{}
	for _, p := range pl.Provisioned {
		fam, err := appx.FamilyNameFromFullName(p.FullName)
		if err != nil {
			continue
		}
		if isBloatFamily(fam, keywords) {
			families[fam] = true
		}
	}

	for fam := range families {
		if err := appx.Remove(pl, fam, true, applications, deprovisioned, root, bt); err != nil {
			log.Printf("warning: appx.Remove(%s): %v", fam, err)
			continue
		}
		fmt.Printf("removed AppX package family: %s\n", fam)
	}

	newData, err := pl.Serialize()
	if err != nil {
		return err
	}
	return writeFile(root, bt, newBlobs, provisioningPath, newData)
}

// isBloatFamily matches familyName (an appx.FamilyNameFromFullName result,
// "<name>_<publisherId>") against bloatAppxKeywords, mirroring
// nano11builder.ps1's PowerShell -like matching against
// Get-AppxProvisionedPackage's PackageName property (the bare package
// name, no publisher suffix) -- checking the whole family name string
// (which still contains the name as its prefix) is equivalent for
// substring matching purposes and avoids re-deriving the bare name.
func isBloatFamily(familyName string, keywords []string) bool {
	lower := strings.ToLower(familyName)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
