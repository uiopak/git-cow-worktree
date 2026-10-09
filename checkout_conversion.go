package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type checkoutKind uint8

const (
	checkoutSkip checkoutKind = iota
	checkoutRaw
	checkoutFiltered
	checkoutFilteredAuto
)

// checkoutConversionKinds reads committed target attributes, even when cloned
// .gitattributes files still contain the source commit's rules. Unknown rules
// and conversions beyond line endings are left to the final Git checkout.
func checkoutConversionKinds(root string, paths []string) ([]checkoutKind, bool) {
	if os.Getenv("GIT_ATTR_SOURCE") != "" {
		return nil, false // checkout must honor the caller's attribute source.
	}
	attributeTree, ok := checkoutConfig(root, "attr.tree")
	if !ok || attributeTree != "" {
		// An explicit attribute tree can override HEAD during checkout.
		return nil, false
	}
	autocrlf, ok := checkoutConfig(root, "core.autocrlf", "--type=bool-or-str")
	if !ok || autocrlf != "" && autocrlf != "false" && autocrlf != "true" && autocrlf != "input" {
		return nil, false
	}
	eol, ok := checkoutConfig(root, "core.eol")
	if !ok || eol != "" && eol != "native" && eol != "lf" && eol != "crlf" {
		return nil, false
	}
	defaultCRLF := autocrlf == "true" || autocrlf != "input" &&
		(eol == "crlf" || (eol == "" || eol == "native") && runtime.GOOS == "windows")
	if kinds, ok := readCheckoutKinds(root, paths, autocrlf, defaultCRLF, false, nil); ok {
		return kinds, true
	}
	return cachedCheckoutKinds(root, paths, autocrlf, defaultCRLF)
}

// Older Git lacks --attr-source. A temporary target index lets --cached
// inspect the same committed attributes without touching the real index.
func cachedCheckoutKinds(root string, paths []string, autocrlf string, defaultCRLF bool) ([]checkoutKind, bool) {
	dir, err := os.MkdirTemp("", "git-cow-worktree-attrs-")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
	cmd := exec.Command("git", "read-tree", "HEAD")
	cmd.Dir, cmd.Env = root, env
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	return readCheckoutKinds(root, paths, autocrlf, defaultCRLF, true, env)
}

func readCheckoutKinds(root string, paths []string, autocrlf string, defaultCRLF bool, cached bool, env []string) ([]checkoutKind, bool) {
	attrs := []string{"text", "eol", "crlf", "filter", "ident", "working-tree-encoding"}
	args := []string{"--attr-source=HEAD", "check-attr", "-z", "--stdin"}
	if cached {
		args = []string{"check-attr", "--cached", "-z", "--stdin"}
	}
	cmd := exec.Command("git", append(args, attrs...)...)
	cmd.Dir, cmd.Env = root, env
	// os/exec copies input while we drain output, avoiding full-pipe deadlocks.
	cmd.Stdin = &checkoutPathReader{paths: paths}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false
	}
	defer out.Close()
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	finished := false
	defer func() {
		if !finished {
			out.Close()
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	r := bufio.NewReader(out)
	kinds := make([]checkoutKind, len(paths))
	for i, path := range paths {
		values := make([]string, len(attrs))
		for j, attr := range attrs {
			p, e1 := readCheckoutField(r)
			a, e2 := readCheckoutField(r)
			v, e3 := readCheckoutField(r)
			if e1 != nil || e2 != nil || e3 != nil || p != path || a != attr {
				return nil, false
			}
			values[j] = v
		}
		kinds[i] = classifyCheckout(values, autocrlf, defaultCRLF)
		// cat-file strips spaces and tabs between the OID and path. Such
		// leading path bytes cannot be represented faithfully in that input.
		if kinds[i] >= checkoutFiltered && (strings.HasPrefix(path, " ") || strings.HasPrefix(path, "\t")) {
			kinds[i] = checkoutSkip
		}
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, false
	}
	err = cmd.Wait()
	finished = true
	return kinds, err == nil
}

func checkoutConfig(root, key string, options ...string) (string, bool) {
	args := append([]string{"config", "--get"}, options...)
	cmd := exec.Command("git", append(args, key)...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		return "", errors.As(err, &exit) && exit.ExitCode() == 1
	}
	return strings.TrimSpace(string(out)), true
}

func classifyCheckout(v []string, autocrlf string, defaultCRLF bool) checkoutKind {
	// check-attr cannot distinguish -filter from a literal filter=unset.
	if v[3] != "unspecified" || v[4] != "unspecified" && v[4] != "unset" {
		return checkoutSkip
	}
	if v[5] != "unspecified" {
		return checkoutSkip
	}
	// text takes precedence over the historical crlf attribute.
	text := v[0]
	// "unset" can mean -text or a literal text=unset, which Git ignores.
	// Use the potentially converting fallback and let Git compare the bytes.
	if text != "set" && text != "auto" && text != "input" {
		text = v[2]
	}
	if text == "unset" {
		text = "unspecified" // -crlf and literal crlf=unset are also ambiguous.
	}
	if v[1] == "crlf" {
		if text == "auto" {
			return checkoutFilteredAuto
		}
		return checkoutFiltered
	}
	if v[1] == "lf" || text == "input" {
		return checkoutRaw
	}
	if text == "set" || text == "auto" {
		if defaultCRLF {
			if text == "auto" {
				return checkoutFilteredAuto
			}
			return checkoutFiltered
		}
		return checkoutRaw
	}
	if autocrlf == "true" {
		return checkoutFilteredAuto
	}
	return checkoutRaw
}

// checkoutPathReader emits paths individually rather than buffering the batch.
type checkoutPathReader struct {
	paths []string
	next  int
	rest  string
}

func (r *checkoutPathReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.rest == "" {
		if r.next == len(r.paths) {
			return 0, io.EOF
		}
		r.rest = r.paths[r.next] + "\x00"
		r.next++
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func readCheckoutField(r *bufio.Reader) (string, error) {
	field, err := r.ReadString(0)
	if err != nil {
		return "", err
	}
	return field[:len(field)-1], nil
}

type checkoutBatch struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
	r   *bufio.Reader
}

func startCheckoutBatch(root string, filtered bool) (*checkoutBatch, error) {
	args := []string{"--attr-source=HEAD", "cat-file", "--batch", "-Z"}
	if filtered {
		args = append(args, "--filters")
	}
	b := &checkoutBatch{cmd: exec.Command("git", args...)}
	b.cmd.Dir = root
	var err error
	b.in, err = b.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	b.out, err = b.cmd.StdoutPipe()
	if err != nil {
		b.in.Close()
		return nil, err
	}
	if err = b.cmd.Start(); err != nil {
		b.in.Close()
		b.out.Close()
		return nil, err
	}
	b.r = bufio.NewReaderSize(b.out, 128*1024)
	return b, nil
}

// close kills a failed batch before waiting, so neither pipe can keep Git
// blocked. Successful batches close input and verify EOF before waiting.
func (b *checkoutBatch) close() {
	if b.cmd.ProcessState == nil {
		b.in.Close()
		b.out.Close()
		b.cmd.Process.Kill()
		b.cmd.Wait()
	}
}

func (b *checkoutBatch) finish() bool {
	if err := b.in.Close(); err != nil {
		return false
	}
	if _, err := b.r.ReadByte(); err != io.EOF {
		return false
	}
	b.out.Close()
	return b.cmd.Wait() == nil
}

func (b *checkoutBatch) request(oid, path string) (int64, bool) {
	input := oid
	if path != "" {
		input += " " + path
	}
	if _, err := io.WriteString(b.in, input+"\x00"); err != nil {
		return 0, false
	}
	head, err := readCheckoutField(b.r)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(head)
	if len(fields) != 3 || fields[0] != oid || fields[1] != "blob" {
		return 0, false
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	return size, err == nil && size >= 0
}

// nulFreeCheckoutPaths preflights raw blobs because --filters reports the raw
// size in its header, not the converted size. NUL-free blobs remain NUL-free
// under line ending conversion, making the filtered record delimiter safe.
// Auto-detected binary blobs can use raw validation. Forced text conversions
// of NUL-bearing blobs are conservatively left to Git.
func nulFreeCheckoutPaths(root string, tgt *treeIndex, paths []string) (map[string]bool, bool) {
	b, err := startCheckoutBatch(root, false)
	if err != nil {
		return nil, false
	}
	defer b.close()
	buf := make([]byte, 128*1024)
	safe := make(map[string]bool, len(paths))
	for _, path := range paths {
		size, ok := b.request(tgt.Blobs[path].SHA, "")
		if !ok {
			return nil, false
		}
		nul := false
		for size > 0 {
			chunk := buf[:min(int64(len(buf)), size)]
			if _, err := io.ReadFull(b.r, chunk); err != nil {
				return nil, false
			}
			nul = nul || bytes.IndexByte(chunk, 0) >= 0
			size -= int64(len(chunk))
		}
		if end, err := b.r.ReadByte(); err != nil || end != 0 {
			return nil, false
		}
		safe[path] = !nul
	}
	return safe, b.finish()
}

// prepareCheckoutConversions moves binary auto-conversion paths back to the
// parallel raw hash workers. A failed preflight leaves all these paths to Git.
func prepareCheckoutConversions(root string, tgt *treeIndex, paths []string, kinds []checkoutKind) []string {
	var filtered []string
	for i, path := range paths {
		if kinds[i] >= checkoutFiltered {
			filtered = append(filtered, path)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	safe, ok := nulFreeCheckoutPaths(root, tgt, filtered)
	filtered = filtered[:0]
	for i, path := range paths {
		if kinds[i] < checkoutFiltered {
			continue
		}
		if ok && safe[path] {
			filtered = append(filtered, path)
		} else if ok && kinds[i] == checkoutFilteredAuto {
			kinds[i] = checkoutRaw
		} else {
			kinds[i] = checkoutSkip
		}
	}
	return filtered
}

func validateCheckoutConversions(root string, tgt *treeIndex, paths []string) []indexEntry {
	if len(paths) == 0 {
		return nil
	}
	b, err := startCheckoutBatch(root, true)
	if err != nil {
		return nil
	}
	defer b.close()
	buf := make([]byte, 128*1024)
	var entries []indexEntry
	for _, path := range paths {
		rawSize, ok := b.request(tgt.Blobs[path].SHA, path)
		if !ok {
			return nil
		}
		e, matched := cloneStatEntry(root, path, tgt.Blobs[path])
		var f *os.File
		if matched {
			f, err = os.Open(filepath.Join(root, path))
			matched = err == nil
		}
		size, matched, complete := compareCheckoutRecord(b.r, f, matched, buf)
		if f != nil {
			f.Close()
		}
		if !complete {
			return nil
		}
		// Line ending conversion only adds bytes. Check the original size too
		// so unsupported protocol behavior cannot vouch for an entry.
		if size < rawSize || size-rawSize > rawSize {
			return nil
		}
		if matched && size == e.Stat.FullSize {
			if st, err := lstatFields(filepath.Join(root, path)); err == nil && st == e.Stat {
				entries = append(entries, e)
			}
		}
	}
	if !b.finish() {
		return nil
	}
	return entries
}

// compareCheckoutRecord always drains the complete record after a file error
// or mismatch. complete distinguishes a protocol failure from a file mismatch.
func compareCheckoutRecord(r *bufio.Reader, f *os.File, matched bool, buf []byte) (int64, bool, bool) {
	var size int64
	for {
		chunk, err := r.ReadSlice(0)
		if err != nil && err != bufio.ErrBufferFull {
			return 0, false, false
		}
		if err == nil {
			chunk = chunk[:len(chunk)-1]
		}
		size += int64(len(chunk))
		if matched {
			actual := buf[:len(chunk)]
			_, readErr := io.ReadFull(f, actual)
			matched = readErr == nil && bytes.Equal(actual, chunk)
		}
		if err == nil {
			if matched {
				n, readErr := f.Read(buf[:1])
				matched = n == 0 && readErr == io.EOF
			}
			return size, matched, true
		}
	}
}
