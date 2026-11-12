package install

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	return code == 259, nil
}

func findRunningClient(dir string) (int, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return 0, err
	}
	root = strings.ToLower(root) + string(os.PathSeparator)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if int(entry.ProcessID) == os.Getpid() {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if e != nil {
			continue
		}
		var buffer [32768]uint16
		size := uint32(len(buffer))
		e = windows.QueryFullProcessImageName(h, 0, &buffer[0], &size)
		windows.CloseHandle(h)
		if e == nil && strings.HasPrefix(strings.ToLower(windows.UTF16ToString(buffer[:size])), root) {
			return int(entry.ProcessID), nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, nil
	}
	return 0, err
}
