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

// provisioningPath is the offline source of truth for provisioned AppX
// packages (see the sibling appx package's own doc comments).
const provisioningPath = `ProgramData\Microsoft\Windows\AppxProvisioning.xml`

func removeBloatAppx(r *wim.Reader, bt *wim.BlobTable, root *wim.DirEntry, software *registry.Hive, newBlobs map[wim.Hash][]byte) error {
	data, err := r.ReadFile(root, bt, provisioningPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", provisioningPath, err)
	}
	pl, err := appx.ParseProvisioning(data)
	if err != nil {
		return err
	}

	applications := software.Hive.Root.FindOrCreatePath(appx.ApplicationsPath)
	deprovisioned := software.Hive.Root.FindOrCreatePath(appx.DeprovisionedPath)

	families := map[string]bool{}
	for _, p := range pl.Provisioned {
		fam, err := appx.FamilyNameFromFullName(p.FullName)
		if err != nil {
			continue
		}
		if isBloatFamily(fam) {
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
func isBloatFamily(familyName string) bool {
	lower := strings.ToLower(familyName)
	for _, kw := range bloatAppxKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
