package main

import (
	_ "embed"
	"fmt"

	"github.com/Pandapip1/gowim/wim"
)

// autounattendXML is nano11's own autounattend.xml (copied verbatim from
// github.com/ntdevlabs/nano11), embedded so this tool has no runtime
// dependency on that repo. nano11builder.ps1 copies this file to
// Windows\System32\Sysprep\autounattend.xml inside the mounted image (see
// its "Enabling Local Accounts on OOBE" section) so Setup skips the
// Microsoft-account requirement; it also ships a copy at the ISO root for
// unattended boot-time setup, which the ISO-authoring stage writes from
// this same embedded copy (see isoimage.go's buildISO).
//
//go:embed autounattend.xml
var autounattendXML []byte

const autounattendSysprepPath = `Windows\System32\Sysprep\autounattend.xml`

func installAutounattend(root *wim.DirEntry, bt *wim.BlobTable, newBlobs map[wim.Hash][]byte) error {
	fmt.Println("Installing autounattend.xml for OOBE local-account bypass...")
	return writeFile(root, bt, newBlobs, autounattendSysprepPath, autounattendXML)
}
