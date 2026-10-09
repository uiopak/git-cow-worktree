package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
)

// indexEntry is one cache entry for the index we hand to Git.
type indexEntry struct {
	Path string
	Mode uint32 // Git's on-disk mode: 0100644, 0100755
	OID  []byte
	Stat fileStat
}

// validateClones verifies every file the plan claims to have cloned and
// returns index entries for those that hold the target checkout bytes.
//
// Identity checkouts are hashed across all cores. Files that may undergo
// line ending conversion are compared with Git's filtered checkout content.
//
// Files that fail verification are left out, and the final checkout rewrites
// them. This covers source changes missed by the stat cache and clones that
// raced with a writer.
func validateClones(root string, tgt *treeIndex, plan clonePlan) []indexEntry {
	cloned := plan.coverage()
	var candidates []string
	for path, te := range tgt.Blobs {
		if !te.IsRegular() {
			continue // symlinks are cheap for Git to write; don't vouch for them
		}
		if cloned.covers(path) {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	kinds, ok := checkoutConversionKinds(root, candidates)
	if !ok {
		return nil // unknown checkout rules must be left to Git
	}
	converted := prepareCheckoutConversions(root, tgt, candidates, kinds)

	entries := make([]indexEntry, len(candidates))
	var wg sync.WaitGroup
	work := make(chan int)
	for range min(runtime.NumCPU(), len(candidates)) {
		wg.Go(func() {
			buf := make([]byte, 128*1024)
			for i := range work {
				if kinds[i] != checkoutRaw {
					continue
				}
				path := candidates[i]
				if e, ok := validateOne(root, path, tgt.Blobs[path], buf); ok {
					entries[i] = e
				}
			}
		})
	}
	for i := range candidates {
		work <- i
	}
	close(work)
	wg.Wait()

	valid := entries[:0]
	for _, e := range entries {
		if e.Path != "" {
			valid = append(valid, e)
		}
	}
	valid = append(valid, validateCheckoutConversions(root, tgt, converted)...)
	return valid
}

func validateOne(root, path string, te TreeEntry, buf []byte) (indexEntry, bool) {
	e, ok := cloneStatEntry(root, path, te)
	if !ok {
		return indexEntry{}, false
	}
	full := filepath.Join(root, path)
	oid, err := hashBlob(full, e.Stat.FullSize, len(te.SHA)/2, buf)
	if err != nil || hex.EncodeToString(oid) != te.SHA {
		return indexEntry{}, false
	}
	if st, err := lstatFields(full); err != nil || st != e.Stat {
		return indexEntry{}, false
	}
	return e, true
}

// cloneStatEntry checks the file type and mode without claiming its content.
func cloneStatEntry(root, path string, te TreeEntry) (indexEntry, bool) {
	full := filepath.Join(root, path)
	st, err := lstatFields(full)
	if err != nil || !st.IsRegular() {
		return indexEntry{}, false
	}
	mode, err := strconv.ParseUint(te.Mode, 8, 32)
	if err != nil {
		return indexEntry{}, false
	}
	// Git records only the executable bit, and derives it from the file.
	// Windows files don't have executable permission bits; keep the tree's
	// mode in the index instead, as Git does with core.filemode=false.
	if wantExec := te.Mode == "100755"; runtime.GOOS != "windows" && wantExec != st.IsExecutable() {
		return indexEntry{}, false
	}
	oid, err := hex.DecodeString(te.SHA)
	if err != nil || len(oid) != sha1.Size && len(oid) != sha256.Size {
		return indexEntry{}, false
	}
	return indexEntry{Path: path, Mode: uint32(mode), OID: oid, Stat: st}, true
}

// hashBlob computes the Git object ID of the file at path, whose size must
// already be known: it goes into the object header, and a file whose size
// changed under us hashes to something that won't match the tree anyway.
func hashBlob(path string, size int64, oidLen int, buf []byte) ([]byte, error) {
	var h hash.Hash
	switch oidLen {
	case sha1.Size:
		h = sha1.New()
	case sha256.Size:
		h = sha256.New()
	default:
		return nil, fmt.Errorf("unsupported object ID length %d", oidLen)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fmt.Fprintf(h, "blob %d\x00", size)
	n, err := io.CopyBuffer(h, f, buf)
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, fmt.Errorf("%s: read %d bytes, want %d", path, n, size)
	}
	return h.Sum(nil), nil
}
