//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestCowWindowsNativeLayout(t *testing.T) {
	var data cowDuplicateExtentsData
	if unsafe.Sizeof(data) != 32 || unsafe.Offsetof(data.SourceFileOffset) != 8 || unsafe.Offsetof(data.TargetFileOffset) != 16 || unsafe.Offsetof(data.ByteCount) != 24 {
		t.Fatalf("DUPLICATE_EXTENTS_DATA does not match the Windows ABI: size=%d offsets=%d/%d/%d", unsafe.Sizeof(data), unsafe.Offsetof(data.SourceFileOffset), unsafe.Offsetof(data.TargetFileOffset), unsafe.Offsetof(data.ByteCount))
	}
}

func TestCowCloneWindowsContentAndIsolation(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	for _, size := range []int{0, 1, 4095, 4096, 4097, 65535, 65536, 65537, 256*1024 + 17} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			src := filepath.Join(dir, fmt.Sprintf("source-%d", size))
			dst := filepath.Join(dir, fmt.Sprintf("clone-%d", size))
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*31 + 7)
			}
			if err := os.WriteFile(src, data, 0o644); err != nil {
				t.Fatal(err)
			}
			mtime := time.Unix(1_700_000_000, 123456700)
			if err := os.Chtimes(src, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			if err := cowClone(src, dst); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dst)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("clone contents differ: %v", err)
			}
			sourceInfo, err := os.Stat(src)
			if err != nil {
				t.Fatal(err)
			}
			cloneInfo, err := os.Stat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if sourceInfo.Size() != int64(size) || cloneInfo.Size() != int64(size) {
				t.Fatalf("logical sizes changed: source=%d clone=%d", sourceInfo.Size(), cloneInfo.Size())
			}
			if os.SameFile(sourceInfo, cloneInfo) || !cloneInfo.ModTime().Equal(sourceInfo.ModTime()) {
				t.Fatal("clone must be a distinct file with the source mtime")
			}
			if size == 0 {
				return
			}
			writeFirstByte := func(path string, value byte) {
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.WriteAt([]byte{value}, 0); err != nil {
					t.Fatal(err)
				}
			}
			writeFirstByte(dst, 42)
			got, err = os.ReadFile(src)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("writing clone changed source: %v", err)
			}
			writeFirstByte(src, 99)
			got, err = os.ReadFile(dst)
			data[0] = 42
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("writing source changed clone: %v", err)
			}
		})
	}
}

func TestCowCloneWindowsAttributes(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	for _, name := range []string{"sparse", "integrity", "sparse-integrity"} {
		t.Run(name, func(t *testing.T) {
			src, dst := filepath.Join(dir, name+"-source"), filepath.Join(dir, name+"-clone")
			data := bytes.Repeat([]byte("block cloning\n"), 1024)
			if err := os.WriteFile(src, data, 0o644); err != nil {
				t.Fatal(err)
			}
			h := windowsTestHandle(t, src, windows.GENERIC_READ|windows.GENERIC_WRITE)
			defer func() { windows.CloseHandle(h) }()
			var returned uint32
			if name != "integrity" {
				if err := windows.DeviceIoControl(h, windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
					t.Fatal(err)
				}
			}
			if name != "sparse" {
				info := struct {
					ChecksumAlgorithm uint16
					Reserved          uint16
					Flags             uint32
				}{ChecksumAlgorithm: 2} // CHECKSUM_TYPE_CRC64
				if err := windows.DeviceIoControl(h, windows.FSCTL_SET_INTEGRITY_INFORMATION, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil, 0, &returned, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := windows.CloseHandle(h); err != nil {
				t.Fatal(err)
			}
			h = windows.InvalidHandle
			if err := cowClone(src, dst); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dst)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("attribute clone contents differ: %v", err)
			}
			dh := windowsTestHandle(t, dst, windows.GENERIC_READ)
			defer windows.CloseHandle(dh)
			var sourceInfo, cloneInfo windows.ByHandleFileInformation
			sh := windowsTestHandle(t, src, windows.GENERIC_READ)
			defer windows.CloseHandle(sh)
			if err := windows.GetFileInformationByHandle(sh, &sourceInfo); err != nil {
				t.Fatal(err)
			}
			if err := windows.GetFileInformationByHandle(dh, &cloneInfo); err != nil {
				t.Fatal(err)
			}
			mask := uint32(windows.FILE_ATTRIBUTE_SPARSE_FILE | windows.FILE_ATTRIBUTE_INTEGRITY_STREAM)
			if sourceInfo.FileAttributes&mask != cloneInfo.FileAttributes&mask {
				t.Fatalf("sparse/integrity attributes differ: source=%x clone=%x", sourceInfo.FileAttributes, cloneInfo.FileAttributes)
			}
		})
	}
}

func TestCowCloneWindowsLargeSparseFile(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	src, dst := filepath.Join(dir, "large-source"), filepath.Join(dir, "large-clone")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
	const size = int64(5)*1024*1024*1024 + 17
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{0, (int64(4) << 30) - 4096, int64(4) << 30, size - 1} {
		if _, err := f.WriteAt([]byte{77}, offset); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cowClone(src, dst); err != nil {
		t.Fatal(err)
	}
	clone, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	info, err := clone.Stat()
	if err != nil || info.Size() != size {
		t.Fatalf("large clone size: info=%v err=%v", info, err)
	}
	for _, offset := range []int64{0, (int64(4) << 30) - 4096, int64(4) << 30, size - 1, size / 2} {
		var got [1]byte
		if _, err := clone.ReadAt(got[:], offset); err != nil {
			t.Fatal(err)
		}
		want := byte(77)
		if offset == size/2 {
			want = 0
		}
		if got[0] != want {
			t.Errorf("byte at %d = %d, want %d", offset, got[0], want)
		}
	}
}

func TestCowCloneWindowsRefusesExistingAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "existing")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cowClone(src, dst); err == nil {
		t.Fatal("clone should refuse an existing destination")
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing destination was changed: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(src, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	dst = filepath.Join(dir, "symlink-clone")
	if err := cowClone(link, dst); err == nil {
		t.Fatal("clone should refuse a source symlink")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed symlink clone left a destination: %v", err)
	}
}

func TestCowCloneWindowsUnsupported(t *testing.T) {
	dir := t.TempDir()
	if cowSupported(t, dir) {
		t.Skip("requires a volume without block cloning")
	}
	for _, data := range []string{"", "content"} {
		src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "clone")
		if err := os.WriteFile(src, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cowClone(src, dst); !isCoWUnsupported(err) {
			t.Fatalf("expected an unsupported error, got %v", err)
		}
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("failed clone left a destination: %v", err)
		}
	}
}

func TestCowCloneWindowsCrossVolumeCleanup(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a source volume with block cloning")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("user cache directory unavailable: %v", err)
	}
	if strings.EqualFold(filepath.VolumeName(cache), filepath.VolumeName(dir)) {
		t.Skip("requires a second volume")
	}
	other, err := os.MkdirTemp(cache, "git-cow-worktree-cross-volume-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(other); err != nil {
			t.Error(err)
		}
	})
	src, dst := filepath.Join(dir, "source"), filepath.Join(other, "clone")
	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cowClone(src, dst); !isCoWUnsupported(err) {
		t.Fatalf("expected cross-volume clone to be unsupported, got %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-volume failure left a destination: %v", err)
	}
}

func TestCowCloneWindowsReadOnlyAndLongPath(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	for range 6 {
		dir = filepath.Join(dir, strings.Repeat("nested", 10))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "clone")
	if err := os.WriteFile(src, []byte("read only source"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(src, 0o644)
	if err := cowClone(src, dst); err != nil {
		t.Fatal(err)
	}
	if st, err := lstatFields(dst); err != nil || !st.IsRegular() {
		t.Fatalf("long clone path could not be statted: st=%+v err=%v", st, err)
	}
	if err := os.WriteFile(dst, []byte("writable clone"), 0o644); err != nil {
		t.Fatalf("clone of a read-only source must be writable: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil || string(got) != "read only source" {
		t.Fatalf("read-only source changed: content=%q err=%v", got, err)
	}
}

func TestWindowsStatLargeSparseFile(t *testing.T) {
	dir := t.TempDir()
	if !cowSupported(t, dir) {
		t.Skip("requires a volume with block cloning")
	}
	path := filepath.Join(dir, "large-stat")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var returned uint32
	if err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{int64(4) << 30, (int64(4) << 30) + 17} {
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		st, err := lstatFields(path)
		if err != nil {
			t.Fatal(err)
		}
		want := uint32(17)
		if size == int64(4)<<30 {
			want = 0x80000000
		}
		if st.FullSize != size || st.Size != want {
			t.Fatalf("large stat: full size=%d index size=%d, want %d/%d", st.FullSize, st.Size, size, want)
		}
	}
}

func TestWindowsStatMatchesGitIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := newRepo(t, "stat")
	r.commit("file.txt", "content")
	path := filepath.Join(r.dir, "file.txt")
	h := windowsTestHandle(t, path, windows.GENERIC_WRITE)
	creation := windows.NsecToFiletime(time.Unix(1_600_000_000, 123456700).UnixNano())
	modified := windows.NsecToFiletime(time.Unix(1_700_000_000, 765432100).UnixNano())
	err := windows.SetFileTime(h, &creation, nil, &modified)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	r.run("git", "update-index", "--index-version", "2", "--refresh")
	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 52 || string(data[:4]) != "DIRC" || binary.BigEndian.Uint32(data[4:8]) != 2 {
		t.Fatalf("unexpected Git index header: %x", data)
	}
	st, err := lstatFields(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{st.CtimeSec, st.CtimeNsec, st.MtimeSec, st.MtimeNsec, st.Dev, st.Ino, 0o100644, st.Uid, st.Gid, st.Size}
	for i, value := range want {
		if got := binary.BigEndian.Uint32(data[12+i*4:]); got != value {
			t.Errorf("index stat field %d = %d, lstatFields = %d", i, got, value)
		}
	}
}

func TestWindowsCloneIndexSurvivesCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r := newRepo(t, "checkout-stat")
	if !cowSupported(t, r.parent) {
		t.Skip("requires a volume with block cloning")
	}
	r.commit("file.txt", "unchanged content\n")
	source := filepath.Join(r.dir, "file.txt")
	old := time.Unix(1_000_000_000, 123456700)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	r.run("git", "update-index", "--refresh")
	out := filepath.Join(r.parent, "clone")
	r.run("git", "worktree", "add", "--no-checkout", "--detach", out, "HEAD")
	clone := filepath.Join(out, "file.txt")
	if err := cowClone(source, clone); err != nil {
		t.Fatal(err)
	}
	entry, ok := validateOne(out, "file.txt", TreeEntry{Mode: "100644", SHA: gitOutput(t, r.dir, "rev-parse", "HEAD:file.txt")}, make([]byte, 1024))
	if !ok {
		t.Fatal("valid clone failed validation")
	}
	index, err := indexPath(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIndex(index, []indexEntry{entry}); err != nil {
		t.Fatal(err)
	}
	fileID := func() [16]byte {
		h := windowsTestHandle(t, clone, windows.GENERIC_READ)
		defer windows.CloseHandle(h)
		var id struct {
			VolumeSerial uint64
			ID           [16]byte
		}
		if err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
			t.Fatal(err)
		}
		return id.ID
	}
	before := fileID()
	for _, cache := range []string{"true", "false"} {
		runIn(t, out, "git", "-c", "core.fscache="+cache, "checkout", "-f", "HEAD")
		info, err := os.Stat(clone)
		if err != nil {
			t.Fatal(err)
		}
		if fileID() != before || !info.ModTime().Equal(old) {
			t.Fatalf("checkout rewrote clone with core.fscache=%s", cache)
		}
	}
	assertIndexUpToDate(t, out, "")
}

func windowsTestHandle(t *testing.T, path string, access uint32) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
