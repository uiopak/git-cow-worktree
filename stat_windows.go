//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Git for Windows uses creation time as ctime, not the native change time.
// Git 2.55's compat/mingw-posix.h converts FILETIME to seconds and nanoseconds
// in 100 ns units; use signed division to match it even before the Unix epoch.
func windowsGitTime(ft windows.Filetime) (sec, nsec uint32) {
	const unixEpoch = int64(116444736000000000)
	ticks := int64(uint64(ft.HighDateTime)<<32|uint64(ft.LowDateTime)) - unixEpoch
	return uint32(ticks / 10000000), uint32(ticks % 10000000 * 100)
}

func lstatFields(path string) (fileStat, error) {
	wpath, err := windowsFilePath(path)
	if err != nil {
		return fileStat{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	// Like mingw_lstat, GetFileAttributesEx returns metadata for the final
	// reparse point itself. No target is opened to obtain these timestamps.
	var info windows.Win32FileAttributeData
	if err := windows.GetFileAttributesEx(wpath, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&info))); err != nil {
		return fileStat{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}

	// Git's file_attr_to_st_mode sets only owner read/write bits and never
	// infers executability from an extension. Treat every reparse point as
	// non-regular, since none can be vouched for by our clone implementation.
	mode := uint32(0o100400)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
		mode |= 0o200
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		mode = mode&0o777 | 0o120000
	} else if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		mode = mode&0o777 | 0o040000
	} else if info.FileAttributes&windows.FILE_ATTRIBUTE_DEVICE != 0 {
		mode = mode&0o777 | 0o020000
	}
	ctimeSec, ctimeNsec := windowsGitTime(info.CreationTime)
	mtimeSec, mtimeNsec := windowsGitTime(info.LastWriteTime)
	size := int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
	return fileStat{
		CtimeSec: ctimeSec, CtimeNsec: ctimeNsec,
		MtimeSec: mtimeSec, MtimeNsec: mtimeNsec,
		// mingw_lstat deliberately zeros dev, ino, uid, and gid.
		Size: gitIndexSize(size), FullSize: size,
		mode: mode,
	}, nil
}
