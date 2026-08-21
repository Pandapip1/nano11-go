package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Pandapip1/gowim/component"
	"github.com/Pandapip1/gowim/registry"
	"github.com/Pandapip1/gowim/wim"
)

// This file is a measurement harness, not a correctness test. It answers one
// question the pipeline's own structure makes non-obvious: how many bytes does
// adding a given servicing-package pattern to packagePatterns actually reclaim,
// given that wipeWinSxS runs *after* removeBloatPackages and already deletes
// essentially all of WinSxS?
//
// Two things make a naive answer wrong, so neither is used here:
//
//  1. Summing file sizes over-counts massively. An install.wim's payload is
//     content-addressed and refcounted: the same blob is referenced from
//     WinSxS and from its hardlinked "projection" into Windows\System32 (and
//     often from several component versions besides). Deleting one reference
//     reclaims nothing. This project has already been bitten by exactly this
//     (see TODO.md's boot.wim locale-trim entry, where a large file-size
//     "saving" turned out to be zero real bytes).
//  2. Measuring a pattern in isolation over-counts too, because a later stage
//     may delete the same content anyway.
//
// So the metric here is the same one wim.RebuildBlobTable computes, which is
// also exactly what determines the output WIM's payload: walk the final,
// fully-mutated tree, collect the *set* of distinct blob hashes it still
// references, and sum each one's uncompressed size once. Hardlinks and
// cross-component sharing therefore cost nothing extra, and a deletion only
// shows up as a saving when it drops the last reference to a blob.
//
// METRIC: uncompressed bytes (ResourceHeader.UncompressedSize), summed over
// distinct SHA-1 blob hashes still referenced by the image tree. Not
// compressed/on-disk WIM bytes -- those depend on the LZX encoder and would
// require a multi-hour re-encode per trial to obtain. Uncompressed bytes are
// the right *relative* measure for comparing candidate patterns.
//
// Two mechanisms, both confirmed against the real image rather than assumed,
// explain why the measured numbers here are so much smaller than a
// file-size-based estimate suggests, and both are the point of measuring:
//
//   - component.Remove, which removeBloatPackages calls, deletes only a
//     KindPackage entry's own `.mum` and its paired `.cat` (see its doc
//     comment). It does NOT delete the package's component payload, because
//     resolving package -> components -> files needs the COMPONENTS-hive/PA30
//     machinery this project deliberately does not reimplement. So a package
//     pattern's whole reclaim budget is those two small XML/catalog files.
//   - Of those two, the `.cat` half reclaims nothing at all. Every
//     servicing\Packages\*.cat blob is also referenced from
//     Windows\System32\CatRoot\{F750E6C3-38EE-11D1-85E5-00C04FC295EE}\ under
//     the same name, and most are referenced a third time from
//     Windows\WinSxS\Catalogs\<sha256>.cat -- and Catalogs is on
//     winSxSKeepPatterns, so it survives the wipe too. Dropping the
//     servicing\Packages reference therefore never drops the last reference.
//     Only the `.mum` bytes are ever actually reclaimed.
//
// Run it against a real image (it is skipped otherwise, so `go test ./...`
// stays clean and fast):
//
//	NANO11_MEASURE_WIM=/path/to/install.wim go test -run TestMeasure -v -timeout 4h .
//
// NANO11_MEASURE_IMAGE selects the 1-based image index (default 6, "Windows 11
// Pro"). The source WIM is only ever opened read-only; nothing is written.

// measureCandidates are the servicing-package identity-prefix patterns whose
// marginal value is being measured, each verified to match at least one real
// Windows\servicing\Packages\*.mum in the test image.
var measureCandidates = []struct {
	name    string
	pattern string
	note    string
}{
	{"Hyper-V (Microsoft-Hyper-V-*)", "Microsoft-Hyper-V-*", ""},
	{"Hyper-V (HyperV-*)", "HyperV-*", "Compute/Feature/Networking/Primitive/OptionalFeature families"},
	{"WSL (Lxss)", "Microsoft-Windows-Lxss-*", ""},
	{"WSL kernel package", "Microsoft-Windows-Kernel-Package-Lxss-Package*", "RISKIER: kernel-adjacent, measured separately and deliberately NOT in packagePatterns"},
	{"Windows Sandbox", "Containers-DisposableClientVM-*", ""},
	{"Application Guard", "Containers-ApplicationGuard-*", ""},
	{"Windows Mobility Center", "Microsoft-Windows-MobilePC-Client-Premium-Package~*", ""},
	{"OneCore Containers", "Microsoft-OneCore-Containers-*", ""},
	{"UtilityVM Containers", "Microsoft-UtilityVM-Containers-*", ""},
	{"Defender AM definitions", "Windows-Defender-AM-Default-Definitions-*", "revived: PS1's Windows-Defender-Client-Package~* matches nothing"},
	{"Defender group policy", "Windows-Defender-Group-Policy-*", "revived"},
	{"Defender App Guard inbox", "Windows-Defender-ApplicationGuard-Inbox-*", "revived"},
	{"Defender SenseClient", "Microsoft-Windows-SenseClient-*", "revived"},
	{"Search engine client", "Microsoft-Windows-SearchEngine-Client-Package*", "revived: PS1's Search-Engine-Client (hyphenated) matches nothing"},
	{"BioEnrollment UX", "Microsoft-Windows-BioEnrollment-UX-Package~*", "revived: PS1's Hello-BioEnrollment matches nothing"},
	{"XPS printing", "Microsoft-Windows-Printing-XpsDocumentWriter-Opt-Package~*", "revived"},
	{"XPS services", "Microsoft-Windows-Printing-XPSServices-Package~*", "revived"},
}

// measureExtraExclusions lists packagePatterns entries that are subsumed by a
// candidate without being spelled identically to one. Together with the
// candidates' own patterns these are dropped from the harness's base pattern
// list, so every candidate's delta is measured against a base that does not
// already remove it. Getting this wrong does not fail loudly -- it silently
// reports a real pattern as reclaiming zero bytes -- so anything added to
// packagePatterns that overlaps a candidate must be listed here too.
var measureExtraExclusions = map[string]bool{
	// all subsumed by the candidate "Microsoft-Windows-Lxss-*"
	"Microsoft-Windows-Lxss-Package~*":        true,
	"Microsoft-Windows-Lxss-merged-Package~*": true,
	"Microsoft-Windows-Lxss-Optional-*":       true,
	"Microsoft-Windows-Lxss-WOW64-Package~*":  true,

	// revived patterns, each now also spelled in packagePatterns
	"Windows-Defender-AM-Default-Definitions-*":                  true,
	"Windows-Defender-Group-Policy-*":                            true,
	"Windows-Defender-ApplicationGuard-Inbox-*":                  true,
	"Microsoft-Windows-SenseClient-*":                            true,
	"Microsoft-Windows-SearchEngine-Client-Package*":             true,
	"Microsoft-Windows-BioEnrollment-UX-Package~*":               true,
	"Microsoft-Windows-Printing-XpsDocumentWriter-Opt-Package~*": true,
	"Microsoft-Windows-Printing-XPSServices-Package~*":           true,
}

// measureEnv is everything a trial needs that can be read from the source WIM
// exactly once and shared, because no trial mutates it: the reader itself, the
// image's chosen metadata resource header, and its XML-declared language and
// architecture.
type measureEnv struct {
	r        *wim.Reader
	metaRes  wim.ResourceHeader
	language string
	arch     string
}

func TestMeasure(t *testing.T) {
	path := os.Getenv("NANO11_MEASURE_WIM")
	if path == "" {
		t.Skip("NANO11_MEASURE_WIM is not set; this is a measurement harness, not a unit test")
	}
	imageIndex := 6
	if s := os.Getenv("NANO11_MEASURE_IMAGE"); s != "" {
		if _, err := fmt.Sscanf(s, "%d", &imageIndex); err != nil {
			t.Fatalf("NANO11_MEASURE_IMAGE=%q: %v", s, err)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	r, err := wim.NewReader(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	bt0, err := r.BlobTable()
	if err != nil {
		t.Fatal(err)
	}
	xd, err := r.XMLData()
	if err != nil {
		t.Fatal(err)
	}
	metaRes := bt0.MetadataResources()
	if imageIndex < 1 || imageIndex > len(metaRes) {
		t.Fatalf("image index %d out of range (%d images)", imageIndex, len(metaRes))
	}
	var img *wim.XMLImage
	for i := range xd.Images {
		if xd.Images[i].Index == imageIndex {
			img = &xd.Images[i]
		}
	}
	if img == nil || img.Windows == nil {
		t.Fatalf("image %d has no <WINDOWS> XML metadata", imageIndex)
	}
	env := &measureEnv{
		r:        r,
		metaRes:  metaRes[imageIndex-1],
		language: img.Windows.DefaultLanguage,
		arch:     img.Windows.ArchitectureName(),
	}
	if env.language == "" {
		env.language = "en-US"
	}
	t.Logf("image %d: %q (%s), language %s, arch %s", imageIndex, img.DisplayName, img.Flags, env.language, env.arch)

	// Sanity-check the metric's own inputs: a solid-resource blob table would
	// make UncompressedSize meaningless for the solid entries (it carries
	// SolidResourceMagic there instead of a real size).
	solid := 0
	for i := range bt0.Entries {
		if bt0.Entries[i].Resource.UncompressedSize == wim.SolidResourceMagic {
			solid++
		}
	}
	t.Logf("blob table: %d entries, %d solid-resource sentinels", len(bt0.Entries), solid)

	base := measureBasePatterns(env.language)
	t.Logf("base pattern list: %d patterns (%d candidate-overlapping patterns excluded)",
		len(base), len(packagePatterns(env.language, false))-len(base))

	// Verify, rather than assume, that the base list really does leave every
	// candidate's packages in place. A pattern already covered by the base
	// would otherwise silently measure as reclaiming zero bytes -- which looks
	// exactly like a genuinely worthless pattern.
	verifyBaseLeavesCandidates(t, env, base)

	type row struct {
		name             string
		pattern          string
		note             string
		matched          int
		naiveMum         int64
		naiveCat         int64
		deltaWithWipe    int64
		deltaWithoutWipe int64
	}

	// Reference points. "pristine" is the untouched image; "current" is the
	// pipeline exactly as main.go runs it today (for an absolute number that
	// can be compared against a real run); "base" is the pipeline with the
	// candidate-overlapping patterns removed, which is what every candidate
	// delta is measured against.
	pristine := measureTrial(t, env, nil, false, false)
	t.Logf("pristine image (no stages):                       %s", humanBytes(pristine))
	current := measureTrial(t, env, packagePatterns(env.language, false), true, true)
	t.Logf("BASELINE, current pipeline as shipped:            %s  (-%s vs pristine)",
		humanBytes(current), humanBytes(pristine-current))
	baseWithWipe := measureTrial(t, env, base, true, true)
	baseNoWipe := measureTrial(t, env, base, true, false)
	t.Logf("base (candidate-overlapping patterns excluded):   %s", humanBytes(baseWithWipe))
	t.Logf("base, WinSxS wipe disabled:                       %s", humanBytes(baseNoWipe))

	// A full sweep is dozens of trials, each dominated by wipeWinSxS (which
	// deletes thousands of WinSxS entries and re-indexes the 95k-entry blob
	// table per deletion) and so takes tens of seconds. NANO11_MEASURE_ONLY
	// takes a comma-separated list of candidate patterns, so a sweep can be run
	// in chunks that each fit inside a normal command timeout rather than as
	// one long-running job. The reference trials are cheap enough to repeat in
	// every chunk, which also cross-checks that chunks are comparable.
	only := map[string]bool{}
	for _, p := range strings.Split(os.Getenv("NANO11_MEASURE_ONLY"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			only[p] = true
		}
	}

	var rows []row
	for _, c := range measureCandidates {
		if len(only) > 0 && !only[c.pattern] {
			continue
		}
		with := append(append([]string(nil), base...), c.pattern)
		n, mumBytes, catBytes := matchedPackageFiles(t, env, c.pattern)
		rows = append(rows, row{
			name:             c.name,
			pattern:          c.pattern,
			note:             c.note,
			matched:          n,
			naiveMum:         mumBytes,
			naiveCat:         catBytes,
			deltaWithWipe:    baseWithWipe - measureTrial(t, env, with, true, true),
			deltaWithoutWipe: baseNoWipe - measureTrial(t, env, with, true, false),
		})
	}

	t.Log("")
	t.Log("marginal bytes reclaimed by adding each pattern, measured on top of the full pipeline")
	t.Log("(WinSxS-wipe column is the one that matters: the wipe runs after package removal)")
	t.Logf("%-38s %5s %12s %12s %14s %14s",
		"pattern", ".mum", "naive .mum", "naive .cat", "REAL w/ wipe", "REAL no wipe")
	for _, x := range rows {
		t.Logf("%-38s %5d %12d %12d %14d %14d", x.pattern, x.matched,
			x.naiveMum, x.naiveCat, x.deltaWithWipe, x.deltaWithoutWipe)
	}
	t.Log("")
	for _, x := range rows {
		if x.note != "" {
			t.Logf("note: %s -- %s", x.pattern, x.note)
		}
	}

	// The headline distinction the harness exists to draw: for each candidate,
	// how much of what it removes survives the WinSxS wipe (is genuinely
	// additive) versus how much the wipe would have reclaimed anyway.
	t.Log("")
	t.Log("additive fraction = (marginal bytes with the wipe) / (marginal bytes without it)")
	for _, x := range rows {
		if x.deltaWithoutWipe == 0 {
			t.Logf("%-42s reclaims nothing either way", x.pattern)
			continue
		}
		t.Logf("%-42s %5.1f%% additive", x.pattern,
			100*float64(x.deltaWithWipe)/float64(x.deltaWithoutWipe))
	}
}

// measureBasePatterns is packagePatterns with every candidate-overlapping
// entry removed (see measureExtraExclusions).
func measureBasePatterns(languageCode string) []string {
	excluded := map[string]bool{}
	for k := range measureExtraExclusions {
		excluded[k] = true
	}
	for _, c := range measureCandidates {
		excluded[c.pattern] = true
	}
	var out []string
	for _, p := range packagePatterns(languageCode, false) {
		if !excluded[p] {
			out = append(out, p)
		}
	}
	return out
}

// measureTrial runs the debloat pipeline against a *fresh* copy of the image's
// tree -- re-parsed from the source WIM's metadata resource every time, since
// every stage mutates the tree in place -- and returns the resulting unique
// referenced blob bytes.
//
// patterns nil means "skip the servicing-package stage entirely"; appx and file
// cleanup run whenever patterns is non-nil (they are what the reported absolute
// numbers are supposed to include), and wipe selects whether wipeWinSxS runs.
func measureTrial(t *testing.T, env *measureEnv, patterns []string, otherStages, wipe bool) int64 {
	t.Helper()
	// A fresh blob table too: the removal stages decrement RefCounts in place.
	// (Those RefCounts are not what the metric reads -- it recounts from the
	// tree -- but the stages mutate them, so sharing one table across trials
	// would let trials interfere.)
	bt, err := env.r.BlobTable()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := env.r.ImageMetadata(env.metaRes)
	if err != nil {
		t.Fatal(err)
	}
	root := meta.Root
	newBlobs := map[wim.Hash][]byte{}

	// Every stage below is chatty on stdout; a trial removes hundreds of
	// packages and thousands of WinSxS entries, and there are two dozen
	// trials. Swallow it (fmt.Print* resolves os.Stdout at call time).
	restore := silenceStdout(t)

	if otherStages {
		hs, err := registry.LoadHiveSet(env.r, root, bt)
		if err != nil {
			restore()
			t.Fatal(err)
		}
		if err := removeBloatAppx(env.r, bt, root, hs.Hives[registry.HiveSoftware], newBlobs, false, false); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	if patterns != nil {
		if err := removePackagesMatching(env.r, bt, root, patterns); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	if otherStages {
		if err := runAggressiveFileCleanup(root, bt, newBlobs, env.arch, stageFlags{winRE: winREKeep}); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	if wipe {
		if err := wipeWinSxS(root, bt, env.arch); err != nil {
			restore()
			t.Fatal(err)
		}
	}
	restore()

	total := uniqueBlobBytes(t, root, bt, newBlobs)

	// Each trial holds a whole freshly-parsed image tree plus a blob table and
	// two hash-keyed maps over ~95k blobs, and there are dozens of trials back
	// to back. Without handing the pages back between them the process's RSS
	// tracks the high-water mark of two live trees rather than one, which on a
	// machine already running VMs was enough to get the run OOM-killed
	// (SIGTERM from systemd-oomd) partway through. Dropping the references and
	// releasing eagerly here costs a few seconds per trial and makes the run
	// survivable alongside other work.
	root, meta, bt, newBlobs = nil, nil, nil, nil
	debug.FreeOSMemory()
	return total
}

// uniqueBlobBytes is the metric: the set of distinct blob hashes root's tree
// still references (exactly what wim.RebuildBlobTable would keep), with each
// one's uncompressed size counted once regardless of how many paths -- WinSxS
// original, System32 hardlink, other component versions -- point at it.
func uniqueBlobBytes(t *testing.T, root *wim.DirEntry, bt *wim.BlobTable, newBlobs map[wim.Hash][]byte) int64 {
	t.Helper()
	sizes := make(map[wim.Hash]uint64, len(bt.Entries))
	for i := range bt.Entries {
		sizes[bt.Entries[i].Hash] = bt.Entries[i].Resource.UncompressedSize
	}
	seen := make(map[wim.Hash]bool, len(bt.Entries))
	var total int64
	var walk func(d *wim.DirEntry)
	walk = func(d *wim.DirEntry) {
		for _, s := range d.Streams {
			if s.Hash.IsZero() || seen[s.Hash] {
				continue
			}
			seen[s.Hash] = true
			if sz, ok := sizes[s.Hash]; ok {
				total += int64(sz)
			} else if data, ok := newBlobs[s.Hash]; ok {
				// A blob this run created (rewritten AppxProvisioning.xml,
				// registry hives): not in the source table, so its size comes
				// from the content itself.
				total += int64(len(data))
			}
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	walk(root)
	return total
}

// matchedPackageFiles reports how many Windows\servicing\Packages\*.mum files
// pattern matches (so a zero delta caused by a pattern matching nothing is
// never mistaken for a pattern whose payload was already reclaimed elsewhere),
// plus the naive "sum of matched file sizes" for the .mum and .cat halves that
// component.Remove deletes. Those naive figures are exactly the numbers a
// file-size-based estimate would report, and are printed alongside the real
// unique-blob deltas to make the gap between them explicit.
func matchedPackageFiles(t *testing.T, env *measureEnv, pattern string) (n int, mumBytes, catBytes int64) {
	t.Helper()
	bt, err := env.r.BlobTable()
	if err != nil {
		t.Fatal(err)
	}
	sizes := make(map[wim.Hash]uint64, len(bt.Entries))
	for i := range bt.Entries {
		sizes[bt.Entries[i].Hash] = bt.Entries[i].Resource.UncompressedSize
	}
	meta, err := env.r.ImageMetadata(env.metaRes)
	if err != nil {
		t.Fatal(err)
	}
	children, err := meta.Root.ReadDir(component.PackagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		name := c.NameUTF8()
		if !wim.MatchName(pattern, name) {
			continue
		}
		sz := int64(sizes[c.MainHash()])
		switch strings.ToLower(name[strings.LastIndexByte(name, '.')+1:]) {
		case "mum":
			n++
			mumBytes += sz
		case "cat":
			catBytes += sz
		}
	}
	return n, mumBytes, catBytes
}

// verifyBaseLeavesCandidates fails the run if applying base already removes any
// candidate's packages, which would make that candidate's measured delta a
// meaningless zero. packagePatterns is edited over time, so this check exists
// to make an out-of-date measureExtraExclusions loud rather than silent.
func verifyBaseLeavesCandidates(t *testing.T, env *measureEnv, base []string) {
	t.Helper()
	bt, err := env.r.BlobTable()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := env.r.ImageMetadata(env.metaRes)
	if err != nil {
		t.Fatal(err)
	}
	restore := silenceStdout(t)
	err = removePackagesMatching(env.r, bt, meta.Root, base)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	children, err := meta.Root.ReadDir(component.PackagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range measureCandidates {
		left := 0
		for _, e := range children {
			if wim.MatchName(c.pattern, e.NameUTF8()) {
				left++
			}
		}
		if left == 0 {
			t.Fatalf("base pattern list already removes everything matching candidate %q; "+
				"add the overlapping packagePatterns entries to measureExtraExclusions", c.pattern)
		}
	}
}

// silenceStdout redirects os.Stdout to /dev/null until the returned function is
// called. Returning a restore func rather than using t.Cleanup matters: trials
// run in a loop within one test, so output must come back between them.
func silenceStdout(t *testing.T) func() {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = devnull
	done := false
	return func() {
		if done {
			return
		}
		done = true
		os.Stdout = saved
		devnull.Close()
	}
}

func humanBytes(n int64) string {
	neg := ""
	v := float64(n)
	if v < 0 {
		neg, v = "-", -v
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%s%d B", neg, int64(v))
	}
	return fmt.Sprintf("%s%.2f %s (%d)", neg, v, units[i], n)
}
