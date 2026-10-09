//go:build windows

package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errCoWUnsupported = errors.New("FSCTL_DUPLICATE_EXTENTS_TO_FILE: copy-on-write unsupported")

// Access, sharing, parameter, and reference-count errors apply to individual
// files. They must not stop attempts to clone the rest of the worktree.
var cowUnsupportedErrnos = []error{
	windows.ERROR_INVALID_FUNCTION,
	windows.ERROR_NOT_SUPPORTED,
	windows.ERROR_NOT_SAME_DEVICE,
}

const canCloneDirs = false

func cowCloneDir(src, dst string) error {
	return errCoWUnsupported
}

// LARGE_INTEGER has eight-byte alignment in the Windows ABI, including on
// 386. Go's 386 alignment differs, so explicitly pad the HANDLE to eight bytes.
type cowDuplicateExtentsData struct {
	FileHandle       windows.Handle
	_                [8 - unsafe.Sizeof(windows.Handle(0))]byte
	SourceFileOffset int64
	TargetFileOffset int64
	ByteCount        int64
}

type cowIntegrityInfo struct {
	ChecksumAlgorithm        uint16
	Reserved                 uint16
	Flags                    uint32
	ChecksumChunkSizeInBytes uint32
	ClusterSizeInBytes       uint32
}

type cowSetIntegrityInfo struct {
	ChecksumAlgorithm uint16
	Reserved          uint16
	Flags             uint32
}

// windowsFilePath gives native calls the same support for long local and UNC
// paths as os.Open. filepath.Abs also removes forward slashes and dot segments
// before adding the extended-length prefix, which disables Win32 normalization.
func windowsFilePath(path string) (*uint16, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(abs, `\\?\`) && !strings.HasPrefix(abs, `\\.\`) {
		if strings.HasPrefix(abs, `\\`) {
			abs = `\\?\UNC\` + abs[2:]
		} else {
			abs = `\\?\` + abs
		}
	}
	return windows.UTF16PtrFromString(abs)
}

// cowVolumeID checks the volume reached by the handle, including paths through
// mount points. ReFS needs its full 64-bit serial number from FILE_ID_INFO.
func cowVolumeID(h windows.Handle) (uint64, error) {
	const fileSupportsBlockRefcounting = 0x08000000
	var flags uint32
	var name [windows.MAX_PATH + 1]uint16
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &flags, &name[0], uint32(len(name))); err != nil {
		return 0, err
	}
	if !strings.EqualFold(windows.UTF16ToString(name[:]), "ReFS") || flags&fileSupportsBlockRefcounting == 0 {
		return 0, errCoWUnsupported
	}
	var id struct {
		VolumeSerialNumber uint64
		FileID             [16]byte
	}
	if err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
		return 0, err
	}
	return id.VolumeSerialNumber, nil
}

func cowGetIntegrity(h windows.Handle) (cowIntegrityInfo, error) {
	var info cowIntegrityInfo
	var returned uint32
	err := windows.DeviceIoControl(h, windows.FSCTL_GET_INTEGRITY_INFORMATION, nil, 0,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), &returned, nil)
	if err == nil && returned != uint32(unsafe.Sizeof(info)) {
		err = fmt.Errorf("FSCTL_GET_INTEGRITY_INFORMATION: returned %d bytes, want %d", returned, unsafe.Sizeof(info))
	}
	return info, err
}

// cowClone shares ReFS extents without changing the source's logical length.
// The source handle refuses to follow the final reparse point and denies writes
// while cloning. dst must not exist; every failure after creation removes it.
func cowClone(src, dst string) (err error) {
	srcPath, err := windowsFilePath(src)
	if err != nil {
		return &os.PathError{Op: "clone source path", Path: src, Err: err}
	}
	sf, err := windows.CreateFile(srcPath, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return &os.PathError{Op: "open clone source", Path: src, Err: err}
	}
	defer windows.CloseHandle(sf)

	var source windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(sf, &source); err != nil {
		return &os.PathError{Op: "stat clone source", Path: src, Err: err}
	}
	kind, err := windows.GetFileType(sf)
	if err != nil {
		return &os.PathError{Op: "type clone source", Path: src, Err: err}
	}
	if kind != windows.FILE_TYPE_DISK || source.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return &os.PathError{Op: "clone source", Path: src, Err: errors.New("source is not a regular file or is a reparse point")}
	}
	sourceVolume, err := cowVolumeID(sf)
	if err != nil {
		return &os.PathError{Op: "query clone source volume", Path: src, Err: err}
	}
	integrity, err := cowGetIntegrity(sf)
	if err != nil {
		return &os.PathError{Op: "query clone source integrity", Path: src, Err: err}
	}
	cluster := int64(integrity.ClusterSizeInBytes)
	const maxChunk = int64(1 << 31) // Strictly less than ReFS's 4 GiB limit.
	if cluster == 0 || cluster&(cluster-1) != 0 || cluster > maxChunk {
		return &os.PathError{Op: "clone source", Path: src, Err: fmt.Errorf("invalid filesystem cluster size %d", cluster)}
	}
	length := uint64(source.FileSizeHigh)<<32 | uint64(source.FileSizeLow)
	if length > uint64(math.MaxInt64-(cluster-1)) {
		return &os.PathError{Op: "clone source", Path: src, Err: errors.New("file size exceeds aligned clone range")}
	}
	roundedLength := (int64(length) + cluster - 1) &^ (cluster - 1)
	chunkLimit := maxChunk &^ (cluster - 1)

	dstPath, err := windowsFilePath(dst)
	if err != nil {
		return &os.PathError{Op: "clone destination path", Path: dst, Err: err}
	}
	df, err := windows.CreateFile(dstPath, windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return &os.PathError{Op: "create clone destination", Path: dst, Err: err}
	}
	defer func() {
		if closeErr := windows.CloseHandle(df); closeErr != nil {
			err = errors.Join(err, &os.PathError{Op: "close clone destination", Path: dst, Err: closeErr})
		}
		if err != nil {
			if removeErr := windows.DeleteFile(dstPath); removeErr != nil {
				err = errors.Join(err, &os.PathError{Op: "remove failed clone", Path: dst, Err: removeErr})
			}
		}
	}()

	destinationVolume, err := cowVolumeID(df)
	if err != nil {
		return &os.PathError{Op: "query clone destination volume", Path: dst, Err: err}
	}
	if sourceVolume != destinationVolume {
		return &os.PathError{Op: "clone", Path: dst, Err: windows.ERROR_NOT_SAME_DEVICE}
	}

	// Make even non-sparse destinations sparse while sizing them, to avoid
	// allocating zero-filled storage that the extent clone would replace.
	var returned uint32
	if err := windows.DeviceIoControl(df, windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &returned, nil); err != nil {
		return &os.PathError{Op: "set clone destination sparse", Path: dst, Err: err}
	}
	destIntegrity, err := cowGetIntegrity(df)
	if err != nil {
		return &os.PathError{Op: "query clone destination integrity", Path: dst, Err: err}
	}
	// The destination can inherit integrity settings from its parent. Copy zero
	// settings too, so an unchecked source can clone into a checked directory.
	if destIntegrity.ChecksumAlgorithm != integrity.ChecksumAlgorithm || destIntegrity.Flags != integrity.Flags {
		settings := cowSetIntegrityInfo{ChecksumAlgorithm: integrity.ChecksumAlgorithm, Flags: integrity.Flags}
		if err := windows.DeviceIoControl(df, windows.FSCTL_SET_INTEGRITY_INFORMATION,
			(*byte)(unsafe.Pointer(&settings)), uint32(unsafe.Sizeof(settings)), nil, 0, &returned, nil); err != nil {
			return &os.PathError{Op: "set clone destination integrity", Path: dst, Err: err}
		}
	}
	if err := windows.Ftruncate(df, int64(length)); err != nil {
		return &os.PathError{Op: "size clone destination", Path: dst, Err: err}
	}

	// Microsoft's CopyOnWrite example sets the exact destination EOF, then
	// rounds only the clone range up to a cluster boundary. ReFS accepts the
	// final partial cluster without extending either file's logical length.
	// https://github.com/microsoft/CopyOnWrite/blob/main/lib/Windows/WindowsCopyOnWriteFilesystem.cs
	for offset := int64(0); offset < roundedLength; {
		count := min(roundedLength-offset, chunkLimit)
		data := cowDuplicateExtentsData{FileHandle: sf, SourceFileOffset: offset, TargetFileOffset: offset, ByteCount: count}
		if err := windows.DeviceIoControl(df, windows.FSCTL_DUPLICATE_EXTENTS_TO_FILE,
			(*byte)(unsafe.Pointer(&data)), uint32(unsafe.Sizeof(data)), nil, 0, &returned, nil); err != nil {
			return &os.PathError{Op: "FSCTL_DUPLICATE_EXTENTS_TO_FILE", Path: dst, Err: err}
		}
		offset += count
	}
	if source.FileAttributes&windows.FILE_ATTRIBUTE_SPARSE_FILE == 0 {
		// Keep ordinary files ordinary after using sparse sizing to avoid
		// allocating temporary blocks before the clone.
		var sparse byte // FILE_SET_SPARSE_BUFFER.SetSparse = FALSE
		if err := windows.DeviceIoControl(df, windows.FSCTL_SET_SPARSE,
			&sparse, 1, nil, 0, &returned, nil); err != nil {
			return &os.PathError{Op: "clear clone destination sparse", Path: dst, Err: err}
		}
	}

	// Preserve the full source timestamp, so a subsequent Git checkout that
	// rewrites a cloned file remains detectable. Creation time belongs to dst.
	if err := windows.SetFileTime(df, nil, nil, &source.LastWriteTime); err != nil {
		return &os.PathError{Op: "set clone destination mtime", Path: dst, Err: err}
	}
	return nil
}
