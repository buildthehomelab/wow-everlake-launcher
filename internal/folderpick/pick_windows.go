package folderpick

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// The Explorer folder picker is IFileOpenDialog with FOS_PICKFOLDERS, called through its COM vtable so the
// launcher stays pure Go.

var (
	ole32                           = syscall.NewLazyDLL("ole32.dll")
	shell32                         = syscall.NewLazyDLL("shell32.dll")
	user32                          = syscall.NewLazyDLL("user32.dll")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoUninitialize              = ole32.NewProc("CoUninitialize")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")
	procGetForegroundWindow         = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessId    = user32.NewProc("GetWindowThreadProcessId")
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

var (
	clsidFileOpenDialog = guid{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = guid{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = guid{0x43826D1E, 0xE718, 0x42EE, [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	rpcEChangedMode         = 0x80010106
	errorCancelled          = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)

	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosPathMustExist   = 0x800
	sigdnFileSysPath   = 0x80058000

	// vtable slots: IUnknown 0-2, IModalWindow::Show 3, then IFileDialog.
	vtRelease        = 2
	vtShow           = 3
	vtSetOptions     = 9
	vtGetOptions     = 10
	vtSetFolder      = 12
	vtSetTitle       = 17
	vtGetResult      = 20
	vtGetDisplayName = 5 // IShellItem
)

// call invokes slot n of a COM object's vtable. Like syscall's Proc.Call, it keeps what the uintptr arguments point
// at alive and in place for the call.
//
//go:uintptrescapes
func call(obj unsafe.Pointer, n int, args ...uintptr) uintptr {
	vtbl := *(**[32]uintptr)(obj)
	r, _, _ := syscall.SyscallN(vtbl[n], append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func pick(title, start string) (string, error) {
	// COM wants the dialog on a thread of its own apartment; the Go scheduler mustn't move this goroutine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if failed(hr) && hr != rpcEChangedMode {
		return "", fmt.Errorf("%w: CoInitializeEx %#x", ErrUnavailable, hr)
	}
	if !failed(hr) {
		defer procCoUninitialize.Call()
	}

	var dlg unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == nil {
		return "", fmt.Errorf("%w: CoCreateInstance %#x", ErrUnavailable, hr)
	}
	defer call(dlg, vtRelease)

	var opts uint32
	call(dlg, vtGetOptions, uintptr(unsafe.Pointer(&opts)))
	call(dlg, vtSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist))
	if t, err := syscall.UTF16PtrFromString(title); err == nil {
		call(dlg, vtSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if start != "" {
		if p, err := syscall.UTF16PtrFromString(start); err == nil {
			var folder unsafe.Pointer
			hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(p)), 0,
				uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&folder)))
			if !failed(hr) && folder != nil {
				call(dlg, vtSetFolder, uintptr(folder))
				call(folder, vtRelease)
			}
		}
	}

	// The launcher's window was in front when its button was pressed: the dialog sits over it, modal to it. A
	// browser's window (the page outside the app window) belongs to another process and isn't made the owner.
	owner, _, _ := procGetForegroundWindow.Call()
	if owner != 0 {
		var pid uint32
		procGetWindowThreadProcessId.Call(owner, uintptr(unsafe.Pointer(&pid)))
		if int(pid) != os.Getpid() {
			owner = 0
		}
	}
	hr = call(dlg, vtShow, owner)
	if uint32(hr) == errorCancelled {
		return "", ErrCancelled
	}
	if failed(hr) {
		return "", fmt.Errorf("folder dialog: %#x", hr)
	}
	var item unsafe.Pointer
	if hr := call(dlg, vtGetResult, uintptr(unsafe.Pointer(&item))); failed(hr) || item == nil {
		return "", fmt.Errorf("folder dialog result: %#x", hr)
	}
	defer call(item, vtRelease)
	var name *uint16
	if hr := call(item, vtGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); failed(hr) || name == nil {
		return "", fmt.Errorf("folder dialog path: %#x", hr)
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	return utf16PtrToString(name), nil
}

func utf16PtrToString(p *uint16) string {
	var s []uint16
	for ptr := unsafe.Pointer(p); ; ptr = unsafe.Add(ptr, 2) {
		c := *(*uint16)(ptr)
		if c == 0 {
			break
		}
		s = append(s, c)
	}
	return syscall.UTF16ToString(s)
}
