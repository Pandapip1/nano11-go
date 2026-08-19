package main

import (
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Pandapip1/gowim/lzx"
	"github.com/Pandapip1/gowim/wim"
)

// winREStubIncludePaths lists the real winre.wim subtrees grafted into the
// donor-based stub, in the order they were added while bisecting Setup's
// SafeOS failures against real setuperr.log evidence (see the winRE
// handling's doc comment): each entry here is a real path Setup's SafeOS
// unmount step (CUnmountWIM/SPCopyFilesAndFolders) was confirmed to require
// by a real install attempt failing with 0x80070003 ("failed to get file
// attributes on source path ...") when it was missing, not a guess.
var winREStubIncludePaths = []string{
	`Windows\Boot`,
}

// winREStubFromDonor builds a single-image, bootable-flagged WIM stub,
// grafting real subtrees (winREStubIncludePaths) copied byte-for-byte out of
// a real winre.wim (donorPath) rather than leaving the root entirely empty.
// Two things confirmed via real install attempts, not guessed, went into
// this shape: (1) the XML data resource must be real, minimal well-formed
// <WIM><IMAGE INDEX="1">...</IMAGE></WIM> text -- Setup's SafeOS mount step
// (wimgapi) rejected an empty XMLData.Document outright with
// "0x8007000D", even though the WIM was otherwise structurally valid; (2)
// a structurally valid but content-empty stub mounts fine but then fails
// when Setup's SafeOS unmount step tries to copy real files back out of it
// ("0x80070003", path not found) -- so real content, added back
// incrementally per winREStubIncludePaths, is genuinely required.
func winREStubFromDonor(donorPath string, lzxOpts lzx.Options) ([]byte, error) {
	f, err := os.Open(donorPath)
	if err != nil {
		return nil, fmt.Errorf("open winre donor %s: %w", donorPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	donor, err := wim.NewReader(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("read winre donor %s: %w", donorPath, err)
	}
	donorBT, err := donor.BlobTable()
	if err != nil {
		return nil, err
	}
	metaResources := donorBT.MetadataResources()
	if len(metaResources) == 0 {
		return nil, fmt.Errorf("winre donor %s has no images", donorPath)
	}
	donorMeta, err := donor.ImageMetadata(metaResources[0])
	if err != nil {
		return nil, err
	}

	// remapper carries SecurityID remapping state across every grafted
	// subtree (currently just one, but built to extend), so a descriptor
	// referenced by more than one path collapses to a single output index
	// rather than being duplicated -- see wim.SecurityRemapper.
	remapper := wim.NewSecurityRemapper()
	newRoot := &wim.DirEntry{Attributes: wim.FileAttributeDirectory, SecurityID: wim.SecurityIDNone, Streams: []wim.Stream{{}}}
	for _, p := range winREStubIncludePaths {
		entry, err := donorMeta.Root.Lookup(p)
		if err != nil {
			return nil, fmt.Errorf("lookup %s in winre donor %s: %w", p, donorPath, err)
		}
		copied := remapper.Copy(entry)
		if err := wim.AttachAt(newRoot, p, copied); err != nil {
			return nil, fmt.Errorf("attach %s in winre stub: %w", p, err)
		}
	}

	images := []*wim.ImageMetadata{{Security: remapper.BuildSecurityData(donorMeta.Security), Root: newRoot}}
	bt, err := wim.RebuildBlobTable(images, donorBT)
	if err != nil {
		return nil, fmt.Errorf("rebuild blob table for winre stub: %w", err)
	}

	// A structurally valid but content-empty <IMAGE> element (tried and
	// fixed here) is not enough either: two independent investigations (a
	// clean-room disassembly of the exact wimgapi.dll extracted from this
	// project's own failing VM, and separately reading wimlib's own
	// xml_add_image()/xml_update_image_info() source) converged on the
	// same real bug. Windows Setup's SafeOS commit path
	// (WIMCommitImageHandle -> StateStoreGetMountedImageTime,
	// mountedimagestore.c line 985) reads back an "Image time low"
	// registry DWORD it wrote at mount time from the image's own
	// <LASTMODIFICATIONTIME> XML; its registry-read helper returns the
	// value itself in eax and signals errors only via SetLastError, but
	// the caller treats a returned value of exactly 0 as failure. An
	// <IMAGE> element with no <LASTMODIFICATIONTIME> resolves to that same
	// 0 and gets misread as a commit failure (0x80004005/E_FAIL), aborting
	// Setup right after the real content graft above got it past mount
	// and file-copy.
	//
	// Base the stub's XML on the donor's own real <IMAGE INDEX="1">
	// element (NAME, WINDOWS, real CREATIONTIME, ...) instead of a bare
	// hand-written one, dropping only <RESOURCES> (whose <OFFSET> values
	// point into the donor file, meaningless for the new one) -- then run
	// wim.RecomputeXMLStats (gowim), mirroring wimlib's own
	// xml_update_image_info(), to refresh DIRCOUNT/FILECOUNT/TOTALBYTES/
	// HARDLINKBYTES/LASTMODIFICATIONTIME for the actually-grafted subtree.
	// RecomputeXMLStats leaves an already-present CREATIONTIME untouched,
	// so the donor's real creation time survives.
	donorImageXML, err := donorImageXML(donor)
	if err != nil {
		return nil, fmt.Errorf("read winre donor XML: %w", err)
	}
	xmlData, err := wim.RecomputeXMLStats(donorImageXML, images, bt, wimTimestampNow())
	if err != nil {
		return nil, fmt.Errorf("recompute winre stub XML stats: %w", err)
	}
	return wim.Assemble(images, bt, xmlData, wim.NewReaderBlobSource(donor, donorBT), wim.WriteOptions{
		CompressionType: wim.HdrFlagCompressLZX,
		ChunkSize:       32768,
		BootIndex:       1,
		GUID:            randomGUID(),
		LZXOptions:      lzxOpts,
	})
}

// donorImageXML returns a single-image <WIM><IMAGE INDEX="1">...</IMAGE>
// </WIM> document copied from the donor's own real image 1 XML, with
// <RESOURCES> stripped (its <OFFSET>/<SIZE> entries describe byte
// positions in the donor file, which are meaningless -- and, since the stub
// is far smaller, likely out of bounds -- in the new one).
func donorImageXML(donor *wim.Reader) (*wim.XMLData, error) {
	xmlData, err := donor.XMLData()
	if err != nil {
		return nil, err
	}
	var root struct {
		Image struct {
			Index int    `xml:"INDEX,attr"`
			Inner string `xml:",innerxml"`
		} `xml:"IMAGE"`
	}
	if err := xml.Unmarshal([]byte(xmlData.Document), &root); err != nil {
		return nil, fmt.Errorf("parse donor XML: %w", err)
	}

	dec := xml.NewDecoder(strings.NewReader(root.Image.Inner))
	var kept strings.Builder
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse donor <IMAGE> content: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		end := dec.InputOffset()
		if se.Name.Local == "RESOURCES" {
			continue
		}
		kept.WriteString(root.Image.Inner[start:end])
	}
	return &wim.XMLData{Document: `<WIM><IMAGE INDEX="1">` + kept.String() + `</IMAGE></WIM>`}, nil
}

// wimTimestampNow returns the current time as a Windows FILETIME (100ns
// ticks since 1601-01-01 UTC), the same convention wim.RecomputeXMLStats and
// DirEntry's own CreationTime/LastWriteTime fields use.
func wimTimestampNow() uint64 {
	const epochDelta = 116444736000000000 // 1601-01-01 to 1970-01-01, in 100ns ticks
	return uint64(time.Now().UnixNano()/100) + epochDelta
}

// randomGUID generates a real random GUID for wim.WriteOptions.GUID, which
// must be explicitly set to a nonzero value (WriteTo/Assemble no longer
// generate one themselves).
func randomGUID() wim.GUID {
	var g wim.GUID
	if _, err := rand.Read(g[:]); err != nil {
		// crypto/rand.Read on a real OS reader essentially never fails;
		// falling through to the zero GUID would just make WriteTo/
		// Assemble reject it outright with a clear error instead of
		// silently misbehaving.
		panic(fmt.Sprintf("randomGUID: %v", err))
	}
	return g
}

const (
	winDir           = `Windows`
	driverRepoDir    = winDir + `\System32\DriverStore\FileRepository`
	fontsDir         = winDir + `\Fonts`
	tasksDir         = winDir + `\System32\Tasks`
	edgeWebViewDir   = winDir + `\System32\Microsoft-Edge-Webview`
	recoveryWimPath  = winDir + `\System32\Recovery\winre.wim`
	oneDriveSetupExe = winDir + `\System32\OneDriveSetup.exe`
	edgeProgramFiles = `Program Files (x86)\Microsoft`
)

// driverStoreRemovePatterns is nano11builder.ps1's $patternsToRemove for
// "slimming the DriverStore" -- non-essential driver classes (printers,
// scanners, multi-function devices, smartcard readers, tape drives, RDP
// virtual bus, Bluetooth PAN).
var driverStoreRemovePatterns = []string{
	"prn*", "scan*", "mfd*", "wscsmd.inf*", "tapdrv*", "rdpbus.inf*", "tdibth.inf*",

	// Beyond the PS1's list. Measured against a real retail Windows 11
	// 25H2 (build 26200) Pro image: after its seven patterns above run,
	// 444.8 MB of DriverStore across 698 driver families still remains.
	// These three groups are the largest pieces of that which the PS1
	// plainly meant to catch but does not:
	//
	//   ntprint / ntprint4   30.9 MB -- ntprint.inf IS the inbox printer
	//       driver, the single biggest printing payload in the image, and
	//       it does not match "prn*". Removing printer drivers is already
	//       the stated intent of prn*/scan*/mfd*.
	//   helloface            71.8 MB -- the Windows Hello face-recognition
	//       biometric driver. packagePatterns already removes the
	//       Hello-Face servicing package, so leaving its driver behind
	//       just strands the payload of an already-disabled feature.
	//   mdm*                 14.5 MB -- 152 legacy dialup modem driver
	//       families (mdmcpq, mdmsupra, mdmgl00*, ...). Verified that all
	//       152 matches really are modem drivers and that no non-modem
	//       family in the image begins with "md", so the glob is not
	//       overreaching. Same legacy-hardware class as the PS1's own
	//       tapdrv* (tape drives).
	//
	// Deliberately NOT included: the vendor Wi-Fi (213.2 MB across 25
	// families) and wired-NIC (64.2 MB across 19) drivers, which are by
	// far the largest remaining block. Cutting those is a different kind
	// of decision from cutting bloat -- it removes hardware enablement, so
	// a machine whose NIC is not one of the few left would come up with no
	// network at all, including during OOBE. Left for an explicit opt-in
	// rather than folded in silently.
	"ntprint.inf*", "ntprint4.inf*", "helloface.inf*", "mdm*",
}

// fontsKeepPatterns/fontsExtraRemove are nano11builder.ps1's font-pruning
// pair: first everything NOT matching a keep pattern is deleted, then a
// further explicit list is deleted even though some of those names
// (segoeuihistoric.ttf) would otherwise have matched a keep pattern.
var fontsKeepPatterns = []string{
	"segoe*.*", "tahoma*.*", "marlett.ttf", "8541oem.fon", "segui*.*",
	"consol*.*", "lucon*.*", "calibri*.*", "arial*.*", "times*.*", "cou*.*", "8*.*",
}
var fontsExtraRemove = []string{
	"mingli*", "msjh*", "msyh*", "malgun*", "meiryo*", "yugoth*", "segoeuihistoric.ttf",
}

// inputMethodDirsToRemove are the CJK IME resource directories
// nano11builder.ps1 deletes outright (Windows\System32\InputMethod\<X>).
var inputMethodDirsToRemove = []string{"CHS", "CHT", "JPN", "KOR"}

// imeDirsToRemove are the CJK trees under Windows\System32\IME, the second
// input-method payload the PS1 leaves behind entirely (see the call site).
var imeDirsToRemove = []string{"IMEJP", "IMETC", "IMEKR"}

// runAggressiveFileCleanup ports nano11builder.ps1's "Performing aggressive
// manual file deletions" section (lines ~166-206) plus the native-image
// and scheduled-task deletions around it: DriverStore slimming, font
// pruning, speech engine/Defender-definition/InputMethod/Temp/Web/Help/
// Cursors removal, Edge + Edge WebView + WinRE + OneDriveSetup.exe
// removal, precompiled .NET native images, and specific scheduled-task
// definition files. Every step is best-effort (matches the PS script's own
// pervasive -ErrorAction SilentlyContinue): a missing file/folder is not
// an error.
// winREMode selects what runAggressiveFileCleanup does to winre.wim -- see
// its call site's doc comment for why this isn't just a bool.
type winREMode int

const (
	winREKeep winREMode = iota
	winREDonorStub
)

func runAggressiveFileCleanup(root *wim.DirEntry, bt *wim.BlobTable, newBlobs map[wim.Hash][]byte, arch string, winRE winREMode, winREDonorPath string, lzxOpts lzx.Options) error {
	fmt.Println("Removing pre-compiled .NET assemblies (Native Images)...")
	if err := removeMatchingChildren(root, bt, winDir+`\assembly`, []string{"NativeImages_*"}, "native images"); err != nil {
		return err
	}

	fmt.Println("Slimming the DriverStore...")
	if err := removeMatchingChildren(root, bt, driverRepoDir, driverStoreRemovePatterns, "driverstore"); err != nil {
		return err
	}

	fmt.Println("Pruning fonts...")
	if err := keepOnlyMatchingChildren(root, bt, fontsDir, fontsKeepPatterns, "fonts (keep-list)"); err != nil {
		return err
	}
	if err := removeMatchingChildren(root, bt, fontsDir, fontsExtraRemove, "fonts (extra remove)"); err != nil {
		return err
	}

	mustRemove := []string{
		winDir + `\Speech\Engines\TTS`,
		`ProgramData\Microsoft\Windows Defender\Definition Updates`,

		// Beyond nano11builder.ps1's own list, measured against a real
		// retail Windows 11 25H2 (build 26200) Pro image by summing unique
		// blob bytes (deduplicated by hash, so hardlinked copies are not
		// double-counted) over the set that survives the WinSxS wipe:
		//
		//   Windows\Speech\Engines\SR          88.3 MB
		//   Windows\Speech_OneCore\Engines     68.6 MB  (TTS 62.3 + SR 6.3)
		//
		// The PS1 removes only Windows\Speech\Engines\TTS (15.5 MB), which
		// is the smallest of the four speech-engine trees -- it misses the
		// speech *recognition* engines next to it and the entire parallel
		// Speech_OneCore tree, together ~10x larger. Removing them is
		// consistent with the language-feature removal the PS1 already
		// does (LanguageFeatures-Speech / -TextToSpeech are in
		// packagePatterns), which disables speech as a feature anyway.
		winDir + `\Speech\Engines\SR`,
		winDir + `\Speech_OneCore\Engines`,

		// There is a third and fourth copy of the speech engines under
		// System32 (7.3 MB together). The sapi.dll / sapi_onecore.dll
		// runtimes beside them are deliberately left alone -- ordinary
		// applications link against those, and this is only meant to cut
		// the voice/recognition data, not break the API surface.
		winDir + `\System32\Speech\Engines`,
		winDir + `\System32\Speech_OneCore\Engines`,

		// Windows Hello, dropped at the user's request. The face-recognition
		// biometric driver itself (71.8 MB) goes via driverStoreRemovePatterns
		// above, and the Hello-Face / BioEnrollment-UX servicing packages go
		// via packagePatterns; these are the two remaining on-disk pieces.
		// The scattered System32 ngc*/webauthn DLLs are deliberately left in
		// place: they total only a few MB and are wired into the credential
		// provider chain, so pulling individual DLLs out from under logon is
		// a poor trade for the size.
		winDir + `\SystemApps\Microsoft.BioEnrollment_cw5n1h2txyewy`,
		winDir + `\System32\WinBioPlugIns`,

		// 82.9 MB, the second largest single item in the image after
		// winre.wim, and pure data rather than code. Inspected rather than
		// assumed: the cab holds 33 JSON files -- one per OEM (Acer, ASUS,
		// Dell, EPSON, HP, Lenovo, LG, MSI, NEC, Panasonic, Samsung,
		// Toshiba, five Generic_OEM_* buckets, ...) plus two schema files
		// and a metadata file. It is the per-OEM confidence data driving
		// the phased rollout of Secure Boot certificate updates, not the
		// updates themselves: the actual payload next to it
		// (dbupdate*.bin, dbxupdate*.bin, KEKUpdateCombined.bin,
		// SKUSiPolicy.P7b, SbatLevel.txt) is about 1 MB in total and is
		// deliberately left in place. Nothing links against the cab, so
		// removing it costs the ability to stage a phased Secure Boot
		// certificate update -- not Secure Boot itself, and not boot.
		winDir + `\System32\SecureBootUpdates\BucketConfidenceData.cab`,

		// Defender Advanced Threat Protection (the "Sense" agent) and the
		// rest of the Program Files-side Defender payload: 302 MB unique
		// by the same measurement, of which 153 MB is the Classification
		// model data (nl7data*/nl7models* DLLs) used for DLP content
		// classification. The PS1 already removes the Defender servicing
		// package (Windows-Defender-Client-Package, which its own comments
		// acknowledge as aggressive) and the 222 MB of signature
		// definitions under ProgramData above, so the on-disk binaries
		// left stranded in Program Files are dead weight in an image that
		// has already had Defender gutted.
		`Program Files\Windows Defender Advanced Threat Protection`,
		`Program Files\Windows Defender`,
		winDir + `\Temp`,
		winDir + `\Web`,
		winDir + `\Help`,
		winDir + `\Cursors`,
		edgeWebViewDir,
		oneDriveSetupExe,
	}
	for _, im := range inputMethodDirsToRemove {
		mustRemove = append(mustRemove, winDir+`\System32\InputMethod\`+im)
	}
	// The PS1 removes System32\InputMethod\{CHS,CHT,JPN,KOR} but leaves the
	// separate System32\IME tree, which is 27.9 MB of the same CJK input
	// method payload: IMEJP 13.4 MB (Japanese), IMETC 4.3 MB (Traditional
	// Chinese), IMEKR 2.9 MB (Korean), and a 7.3 MB SHARED directory. Cut
	// the three language trees for consistency with the InputMethod
	// removal above and with the CJK IME package/AppX removal; SHARED is
	// left in place as cheap insurance, since 7.3 MB is not worth guessing
	// about what else might link against it.
	for _, ime := range imeDirsToRemove {
		mustRemove = append(mustRemove, winDir+`\System32\IME\`+ime)
	}
	for _, path := range mustRemove {
		if err := removeIfExists(root, bt, path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	fmt.Println("Removing Edge (Program Files (x86)\\Microsoft\\Edge*)...")
	if err := removeMatchingChildren(root, bt, edgeProgramFiles, []string{"Edge*"}, "edge"); err != nil {
		return err
	}

	fmt.Println("Removing Edge WebView WinSxS component...")
	if err := removeMatchingChildren(root, bt, `Windows\WinSxS`,
		[]string{arch + "_microsoft-edge-webview_31bf3856ad364e35*"}, "edge webview winsxs"); err != nil {
		return err
	}

	// nano11builder.ps1 (the real PowerShell script this port mirrors)
	// deletes winre.wim and recreates it as a zero-byte placeholder file in
	// the same spot. That empty-but-present file breaks a real clean
	// install driven by Setup.exe (as opposed to a raw WinPE dism
	// /apply-image): Setup's SafeOS phase stages it to
	// C:\$WINDOWS.~BT\Sources\SafeOS\winre.wim and tries to mount it to
	// build the recovery/rollback environment, which fails against a
	// 0-byte file (0x8007000B, "the data is invalid") and aborts Setup
	// entirely -- matching real, independent tiny11builder GitHub reports
	// of the same SafeOS/winre.wim failure after this exact trick
	// (ntdevlabs/tiny11builder issues #121 and discussion #466).
	//
	// Deleting the entry outright instead of stubbing it is NOT safer
	// (tried and removed as an option here, not just theorized): confirmed
	// via a real install attempt's setuperr.log, Setup's SafeOS phase
	// unconditionally tries to *extract*
	// \Windows\System32\Recovery\winre.wim out of install.wim before it
	// ever gets to mounting anything, and fails just as hard when the file
	// is simply missing (CExtractFilesFromWIM::DoExecute, error
	// 0x80070003, "the system cannot find the path specified").
	//
	// A structurally valid but content-empty stub (tried and removed as an
	// option here) is NOT sufficient either, for two reasons confirmed via
	// real install attempts, in order:
	//  1. An empty XMLData.Document (wim.Assemble's own zero value) made
	//     Setup's SafeOS mount step itself fail: "Failed to open WIM file
	//     ... Error: 0x8007000D" -- a real WIM's XML data is never empty,
	//     and wimgapi's mount path evidently parses it.
	//  2. Even with well-formed XML, mounting succeeds but Setup's SafeOS
	//     unmount step then fails trying to copy files back OUT of the
	//     mounted image: "SPCopyFilesAndFolders: Failed to get file
	//     attributes on source path C:\$WINDOWS.~BT\Sources\SafeOS\SafeOS
	//     .Mount\WINDOWS\Boot. hr = 0x80070003" -- an empty root has no
	//     such path. This is exactly the bisection this project's user
	//     asked for: graft real content from a genuine winre.wim back in,
	//     incrementally, until Setup stops asking for more (see
	//     winREStubFromDonor/winREStubIncludePaths) rather than guessing
	//     everything needed up front.
	switch winRE {
	case winREKeep:
		fmt.Println("Skipping winre.wim removal (unsafe before a Setup.exe-driven install by default -- see comment, pass -winre-mode to opt into an alternative)")
	case winREDonorStub:
		fmt.Printf("Replacing winre.wim with a stub grafted from %s (-winre-mode=donor-stub; bisection in progress, see comment)...\n", winREDonorPath)
		stub, err := winREStubFromDonor(winREDonorPath, lzxOpts)
		if err != nil {
			return fmt.Errorf("build donor winre.wim stub: %w", err)
		}
		if err := writeFile(root, bt, newBlobs, recoveryWimPath, stub); err != nil {
			return fmt.Errorf("write donor winre.wim stub: %w", err)
		}
	}

	fmt.Println("Deleting scheduled task definition files...")
	taskFiles := []string{
		tasksDir + `\Microsoft\Windows\Application Experience\Microsoft Compatibility Appraiser`,
		tasksDir + `\Microsoft\Windows\Customer Experience Improvement Program`,
		tasksDir + `\Microsoft\Windows\Application Experience\ProgramDataUpdater`,
		tasksDir + `\Microsoft\Windows\Chkdsk\Proxy`,
		tasksDir + `\Microsoft\Windows\Windows Error Reporting\QueueReporting`,
	}
	for _, path := range taskFiles {
		if err := removeIfExists(root, bt, path); err != nil {
			return fmt.Errorf("remove task %s: %w", path, err)
		}
	}

	return nil
}
