package main

import (
	"fmt"

	"github.com/Pandapip1/gowim/component"
	"github.com/Pandapip1/gowim/wim"
)

// winSxSKeepPatterns is a direct translation of nano11builder.ps1's
// $dirsToCopy list: nano11 copies just these WinSxS entries to a sibling
// WinSxS_edit directory, deletes the entire real WinSxS folder, then
// renames WinSxS_edit back to WinSxS -- i.e. every WinSxS entry NOT
// matching one of these patterns is gone. This is explicitly the most
// aggressive, most likely-to-break-something step in the whole script
// (per nano11's own README/comments): it leaves the COMPONENTS hive
// (package/component bookkeeping) permanently inconsistent with the
// actual WinSxS contents, which is exactly the "breakage" the user asked
// to port faithfully rather than have this tool quietly soften. See
// gowim's TODO.md "CBS/servicing package subsystem" research entry for
// why the COMPONENTS hive itself is left untouched either way (its
// internal schema is undocumented and not safely mutable -- this step
// doesn't touch it, just the files it (no longer accurately) describes).
var winSxSKeepPatterns = map[string][]string{
	"amd64": {
		"x86_microsoft.windows.common-controls_6595b64144ccf1df_*",
		"x86_microsoft.windows.gdiplus_6595b64144ccf1df_*",
		"x86_microsoft.windows.i..utomation.proxystub_6595b64144ccf1df_*",
		"x86_microsoft.windows.isolationautomation_6595b64144ccf1df_*",
		"x86_microsoft-windows-s..ngstack-onecorebase_31bf3856ad364e35_*",
		"x86_microsoft-windows-s..stack-termsrv-extra_31bf3856ad364e35_*",
		"x86_microsoft-windows-servicingstack_31bf3856ad364e35_*",
		"x86_microsoft-windows-servicingstack-inetsrv_*",
		"x86_microsoft-windows-servicingstack-onecore_*",
		"amd64_microsoft.vc80.crt_1fc8b3b9a1e18e3b_*",
		"amd64_microsoft.vc90.crt_1fc8b3b9a1e18e3b_*",
		"amd64_microsoft.windows.c..-controls.resources_6595b64144ccf1df_*",
		"amd64_microsoft.windows.common-controls_6595b64144ccf1df_*",
		"amd64_microsoft.windows.gdiplus_6595b64144ccf1df_*",
		"amd64_microsoft.windows.i..utomation.proxystub_6595b64144ccf1df_*",
		"amd64_microsoft.windows.isolationautomation_6595b64144ccf1df_*",
		"amd64_microsoft-windows-s..stack-inetsrv-extra_31bf3856ad364e35_*",
		"amd64_microsoft-windows-s..stack-msg.resources_31bf3856ad364e35_*",
		"amd64_microsoft-windows-s..stack-termsrv-extra_31bf3856ad364e35_*",
		"amd64_microsoft-windows-servicingstack_31bf3856ad364e35_*",
		"amd64_microsoft-windows-servicingstack-inetsrv_31bf3856ad364e35_*",
		"amd64_microsoft-windows-servicingstack-msg_31bf3856ad364e35_*",
		"amd64_microsoft-windows-servicingstack-onecore_31bf3856ad364e35_*",
		"Catalogs",
		"FileMaps",
		"Fusion",
		"InstallTemp",
		"Manifests",
		"x86_microsoft.vc80.crt_1fc8b3b9a1e18e3b_*",
		"x86_microsoft.vc90.crt_1fc8b3b9a1e18e3b_*",
		"x86_microsoft.windows.c..-controls.resources_6595b64144ccf1df_*",
	},
	"arm64": {
		"arm64_microsoft-windows-servicingstack-onecore_31bf3856ad364e35_*",
		"Catalogs",
		"FileMaps",
		"Fusion",
		"InstallTemp",
		"Manifests",
		"SettingsManifests",
		"Temp",
		"x86_microsoft.vc80.crt_1fc8b3b9a1e18e3b_*",
		"x86_microsoft.vc90.crt_1fc8b3b9a1e18e3b_*",
		"x86_microsoft.windows.c..-controls.resources_6595b64144ccf1df_*",
		"x86_microsoft.windows.common-controls_6595b64144ccf1df_*",
		"x86_microsoft.windows.gdiplus_6595b64144ccf1df_*",
		"x86_microsoft.windows.i..utomation.proxystub_6595b64144ccf1df_*",
		"x86_microsoft.windows.isolationautomation_6595b64144ccf1df_*",
		"arm_microsoft.windows.c..-controls.resources_6595b64144ccf1df_*",
		"arm_microsoft.windows.common-controls_6595b64144ccf1df_*",
		"arm_microsoft.windows.gdiplus_6595b64144ccf1df_*",
		"arm_microsoft.windows.i..utomation.proxystub_6595b64144ccf1df_*",
		"arm_microsoft.windows.isolationautomation_6595b64144ccf1df_*",
		"arm64_microsoft.vc80.crt_1fc8b3b9a1e18e3b_*",
		"arm64_microsoft.vc90.crt_1fc8b3b9a1e18e3b_*",
		"arm64_microsoft.windows.c..-controls.resources_6595b64144ccf1df_*",
		"arm64_microsoft.windows.common-controls_6595b64144ccf1df_*",
		"arm64_microsoft.windows.gdiplus_6595b64144ccf1df_*",
		"arm64_microsoft.windows.i..utomation.proxystub_6595b64144ccf1df_*",
		"arm64_microsoft.windows.isolationautomation_6595b64144ccf1df_*",
		"arm64_microsoft-windows-servicing-adm_31bf3856ad364e35_*",
		"arm64_microsoft-windows-servicingcommon_31bf3856ad364e35_*",
		"arm64_microsoft-windows-servicing-onecore-uapi_31bf3856ad364e35_*",
		"arm64_microsoft-windows-servicingstack_31bf3856ad364e35_*",
		"arm64_microsoft-windows-servicingstack-inetsrv_31bf3856ad364e35_*",
		"arm64_microsoft-windows-servicingstack-msg_31bf3856ad364e35_*",
	},
}

// wipeWinSxS deletes every WinSxS entry not matching winSxSKeepPatterns[arch]
// (nano11's copy-to-WinSxS_edit-then-rename dance has no purpose in an
// in-memory tree -- deleting everything that wouldn't have been copied
// achieves the identical end state directly). arch is the WIM XML's
// ArchitectureName() (e.g. "x64", remapped to "amd64" to match nano11's own
// remap, or "arm64"). Unrecognized architectures (x86, arm) are left
// untouched entirely, matching nano11's own script, which only defines
// dirsToCopy for amd64/arm64.
func wipeWinSxS(root *wim.DirEntry, bt *wim.BlobTable, arch string) error {
	if arch == "x64" {
		arch = "amd64"
	}
	patterns, ok := winSxSKeepPatterns[arch]
	if !ok {
		fmt.Printf("wipeWinSxS: no keep-list defined for architecture %q, skipping\n", arch)
		return nil
	}
	return keepOnlyMatchingChildren(root, bt, component.WinSxSDir, patterns, "wipeWinSxS")
}
