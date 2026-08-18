package main

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Pandapip1/gowim/wim"
)

// combinedBlobSource serves a blob from overrides (newly written content
// this run produced, e.g. modified hives/AppxProvisioning.xml) if present,
// falling back to an existing WIM's blob table/reader for everything else
// (the vast majority of an install.wim's content, entirely unmodified).
type combinedBlobSource struct {
	overrides map[wim.Hash][]byte
	fallback  wim.BlobSource
}

func (c combinedBlobSource) Blob(h wim.Hash) ([]byte, error) {
	if data, ok := c.overrides[h]; ok {
		return data, nil
	}
	return c.fallback.Blob(h)
}

// addBlob registers a newly-written blob's hash in bt (deduping against an
// existing entry, incrementing its RefCount, if the new content happens to
// match one already present) and decrements oldHash's RefCount by one (the
// stream this blob is replacing), mirroring registry.Hive.Save's own
// dedup/refcount bookkeeping. oldHash may be the zero hash (a brand-new
// file, not a replacement).
func addBlob(bt *wim.BlobTable, oldHash, newHash wim.Hash) {
	if !oldHash.IsZero() && oldHash != newHash {
		for i := range bt.Entries {
			if bt.Entries[i].Hash == oldHash && bt.Entries[i].RefCount > 0 {
				bt.Entries[i].RefCount--
				break
			}
		}
	}
	if oldHash == newHash {
		return
	}
	for i := range bt.Entries {
		if bt.Entries[i].Hash == newHash {
			bt.Entries[i].RefCount++
			return
		}
	}
	bt.Entries = append(bt.Entries, wim.BlobDescriptor{Hash: newHash, RefCount: 1, PartNumber: 1})
}

// writeFile replaces (or creates) the file at path with data's content,
// registering the new blob in bt/newBlobs and decrementing whatever it
// replaced. This is the shared primitive every step that injects/rewrites
// a file (AppxProvisioning.xml, autounattend.xml, the winre.wim stub, ...)
// uses, so blob-table bookkeeping is handled in exactly one place.
func writeFile(root *wim.DirEntry, bt *wim.BlobTable, newBlobs map[wim.Hash][]byte, path string, data []byte) error {
	var oldHash wim.Hash
	if old, err := root.Lookup(path); err == nil {
		oldHash = old.MainHash()
	} else if !errors.Is(err, wim.ErrNotFound) {
		return fmt.Errorf("lookup %s: %w", path, err)
	}

	newHash := wim.Hash(sha1.Sum(data))
	if _, err := root.Add(path, newHash); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	addBlob(bt, oldHash, newHash)
	if len(data) > 0 {
		newBlobs[newHash] = data
	}
	return nil
}

// removeIfExists deletes path (file or whole directory subtree) from
// root's tree, decrementing bt's blob refcounts for anything removed, and
// treating "already gone" as success -- mirroring nano11builder.ps1's
// pervasive `-ErrorAction SilentlyContinue` on its Remove-Item calls.
func removeIfExists(root *wim.DirEntry, bt *wim.BlobTable, path string) error {
	entry, err := root.Lookup(path)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("lookup %s: %w", path, err)
	}
	decrementBlobRefs(bt, entry)
	if err := root.Remove(path); err != nil && !errors.Is(err, wim.ErrNotFound) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// decrementBlobRefs walks removed and its full subtree, decrementing, in
// bt, the RefCount of every blob-table entry whose Hash matches one of
// removed's streams -- the same duplicated helper the sibling driver/appx/
// component packages each carry (see driver/uninstall.go's doc comment for
// the full never-reclaim/never-underflow reasoning).
func decrementBlobRefs(bt *wim.BlobTable, removed *wim.DirEntry) {
	if bt == nil || removed == nil {
		return
	}
	index := make(map[wim.Hash]*wim.BlobDescriptor, len(bt.Entries))
	for i := range bt.Entries {
		index[bt.Entries[i].Hash] = &bt.Entries[i]
	}
	var walk func(d *wim.DirEntry)
	walk = func(d *wim.DirEntry) {
		for _, s := range d.Streams {
			if s.Hash.IsZero() {
				continue
			}
			if desc, ok := index[s.Hash]; ok && desc.RefCount > 0 {
				desc.RefCount--
			}
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	walk(removed)
}

// removeMatchingChildren deletes every immediate child of dir (an
// already-navigated directory path) whose name matches any of patterns
// (DOS-style globs, case-insensitive -- see wim.MatchName), logging each
// one. A dir that doesn't exist at all is silently skipped.
func removeMatchingChildren(root *wim.DirEntry, bt *wim.BlobTable, dir string, patterns []string, label string) error {
	children, err := root.ReadDir(dir)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, c := range children {
		name := c.NameUTF8()
		for _, pat := range patterns {
			if wim.MatchName(pat, name) {
				fmt.Printf("%s: removing %s\n", label, name)
				if err := removeIfExists(root, bt, dir+`\`+name); err != nil {
					log.Printf("warning: remove %s\\%s: %v", dir, name, err)
				}
				break
			}
		}
	}
	return nil
}

// keepOnlyMatchingChildren is removeMatchingChildren's inverse: it deletes
// every immediate child of dir whose name does NOT match any of
// keepPatterns.
func keepOnlyMatchingChildren(root *wim.DirEntry, bt *wim.BlobTable, dir string, keepPatterns []string, label string) error {
	children, err := root.ReadDir(dir)
	if err != nil {
		if errors.Is(err, wim.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	removed := 0
	for _, c := range children {
		name := c.NameUTF8()
		keep := false
		for _, pat := range keepPatterns {
			if wim.MatchName(pat, name) {
				keep = true
				break
			}
		}
		if keep {
			continue
		}
		if err := removeIfExists(root, bt, dir+`\`+name); err != nil {
			log.Printf("warning: remove %s\\%s: %v", dir, name, err)
			continue
		}
		removed++
	}
	fmt.Printf("%s: removed %d of %d entries under %s\n", label, removed, len(children), dir)
	return nil
}

// hasPrefixFold is strings.HasPrefix, case-insensitively.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
