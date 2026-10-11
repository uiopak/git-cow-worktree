//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runProcess(exe string, args, env []string, containTree bool) (int, error) {
	var job windows.Handle
	if containTree {
		var err error
		job, err = windows.CreateJobObject(nil, nil)
		if err != nil {
			return 0, fmt.Errorf("create cancellation job: %w", err)
		}
		// Only routed COW operations own their descendants. Ordinary Git may
		// start persistent services such as fsmonitor that must outlive the shim.
		// The non-inheritable job handle also kills COW descendants if the shim
		// is forcibly terminated.
		defer windows.CloseHandle(job)
		limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
			return 0, fmt.Errorf("set cancellation job limits: %w", err)
		}
	}

	// Duplicate actual standard handles, including redirected pipes, and inherit
	// only these three. Other handles in the launcher must not leak into Git.
	var handles []windows.Handle
	defer func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}()
	for i, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		handle, err := inheritedStandardHandle(file, i == 0)
		if err != nil {
			return 0, fmt.Errorf("prepare standard handle %d: %w", i, err)
		}
		handles = append(handles, handle)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return 0, err
	}
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = handles[0], handles[1], handles[2]
	si.ProcThreadAttributeList = attrs.List()
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		return 0, err
	}
	block, err := environmentBlock(env)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(app, command, nil, nil, true, flags, &block[0], nil, &si.StartupInfo, &pi); err != nil {
		return 0, fmt.Errorf("start real Git: %w", err)
	}
	runtime.KeepAlive(handles)
	runtime.KeepAlive(block)
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if job != 0 {
		if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
			// No instruction has executed yet. Assignment failure must never run an
			// uncontained child or fall back to an ordinary checkout.
			windows.TerminateProcess(pi.Process, 1)
			return 0, fmt.Errorf("assign real Git to cancellation job: %w", err)
		}
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return 0, fmt.Errorf("resume real Git: %w", err)
	}
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	// Converting to int also preserves all 32 exit-code bits on Windows/386.
	return int(code), nil
}

func inheritedStandardHandle(file *os.File, input bool) (windows.Handle, error) {
	var handle windows.Handle
	source := windows.InvalidHandle
	if file != nil {
		source = windows.Handle(file.Fd())
	}
	if source != 0 && source != windows.InvalidHandle {
		err := windows.DuplicateHandle(windows.CurrentProcess(), source, windows.CurrentProcess(), &handle, 0, true, windows.DUPLICATE_SAME_ACCESS)
		if err == nil {
			return handle, nil
		}
		if !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			return 0, err
		}
	}
	// GUI launchers can provide no standard handles. Give the child valid,
	// inheritable NUL handles while keeping real console/pipe handles intact.
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	access := uint32(windows.GENERIC_WRITE)
	if input {
		access = windows.GENERIC_READ
	}
	security := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	return windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &security, windows.OPEN_EXISTING, 0, 0)
}

func environmentBlock(env []string) ([]uint16, error) {
	entries := append([]string(nil), env...)
	sort.SliceStable(entries, func(i, j int) bool { return strings.ToUpper(entries[i]) < strings.ToUpper(entries[j]) })
	var block []uint16
	for _, entry := range entries {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, fmt.Errorf("environment contains NUL")
		}
		block = append(block, utf16.Encode([]rune(entry))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	if len(entries) == 0 {
		block = append(block, 0)
	}
	return block, nil
}
