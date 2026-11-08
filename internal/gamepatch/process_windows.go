//go:build windows

package gamepatch

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	ntdll                     = syscall.NewLazyDLL("ntdll.dll")
	procReadProcessMemory     = kernel32.NewProc("ReadProcessMemory")
	procWriteProcessMemory    = kernel32.NewProc("WriteProcessMemory")
	procVirtualQueryEx        = kernel32.NewProc("VirtualQueryEx")
	procVirtualProtectEx      = kernel32.NewProc("VirtualProtectEx")
	procNtQueryInformationPro = ntdll.NewProc("NtQueryInformationProcess")
)

const (
	processVMOperation         = 0x0008
	processVMRead              = 0x0010
	processVMWrite             = 0x0020
	processQueryInformation    = 0x0400
	memCommit                  = 0x1000
	memPrivate                 = 0x20000
	pageNoAccess               = 0x01
	pageReadWrite              = 0x04
	pageGuard                  = 0x100
	writable                   = 0x04 | 0x08 | 0x40 | 0x80 // PAGE_READWRITE, WRITECOPY, EXECUTE_READWRITE, EXECUTE_WRITECOPY
	stillActive                = 259
	processBasicInformationCls = 0
)

// memoryBasicInformation is MEMORY_BASIC_INFORMATION for 64-bit processes.
type memoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	PartitionID       uint16
	_                 uint16
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

// Process is another process's memory, through ReadProcessMemory / WriteProcessMemory.
type Process struct {
	h syscall.Handle
}

// OpenProcess opens a process by id with the rights the patches need.
func OpenProcess(pid int) (*Process, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, fmt.Errorf("the game patches need a 64-bit launcher")
	}
	// The rights Karrz's launcher opens the live client with, and no more: no suspend/resume.
	h, err := syscall.OpenProcess(processVMOperation|processVMRead|processVMWrite|processQueryInformation, false,
		uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open the game process: %w", err)
	}
	return &Process{h: h}, nil
}

// Close releases the process handle.
func (p *Process) Close() { syscall.CloseHandle(p.h) }

func (p *Process) query(addr uint64) (memoryBasicInformation, bool) {
	var mbi memoryBasicInformation
	r, _, _ := procVirtualQueryEx.Call(uintptr(p.h), uintptr(addr), uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
	return mbi, r != 0
}

// Regions lists the committed private read/write regions (heap memory).
func (p *Process) Regions() [][2]uint64 {
	var out [][2]uint64
	var addr uint64
	for {
		mbi, ok := p.query(addr)
		if !ok {
			break
		}
		base, size := uint64(mbi.BaseAddress), uint64(mbi.RegionSize)
		if mbi.State == memCommit && mbi.Type == memPrivate && mbi.Protect == pageReadWrite {
			out = append(out, [2]uint64{base, base + size})
		}
		next := base + size
		if next <= addr {
			break
		}
		addr = next
	}
	return out
}

func (p *Process) Read(addr uint64, n int) []byte {
	if n <= 0 {
		return nil
	}
	b := p.ReadInto(addr, make([]byte, n))
	if len(b) == 0 {
		return nil
	}
	return b
}

func (p *Process) ReadInto(addr uint64, buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}
	var got uintptr
	procReadProcessMemory.Call(uintptr(p.h), uintptr(addr), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		uintptr(unsafe.Pointer(&got)))
	return buf[:got]
}

// Write writes data at addr, only where the game's own memory is writable already. It never changes a page's
// protection: the live client's protection (Wow_loader.dll) notices a page of its image being made writable, even when
// the bytes are put back, and ends the game with a "Security Crash" about 30 seconds after start (alpha.7 and
// alpha.11, 2026-11-08).
func (p *Process) Write(addr uint64, data []byte) bool {
	if len(data) == 0 {
		return true
	}
	for a := addr &^ 0xfff; a < addr+uint64(len(data)); a += 0x1000 {
		mbi, ok := p.query(a)
		if !ok || mbi.State != memCommit || mbi.Protect&pageGuard != 0 || mbi.Protect&writable == 0 {
			return false
		}
	}
	var n uintptr
	r, _, _ := procWriteProcessMemory.Call(uintptr(p.h), uintptr(addr), uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)), uintptr(unsafe.Pointer(&n)))
	return r != 0 && int(n) == len(data)
}

// Alive reports whether the process is still running.
func (p *Process) Alive() bool {
	var code uint32
	if err := syscall.GetExitCodeProcess(p.h, &code); err != nil {
		return false
	}
	return code == stillActive
}

func ntStatus(r uintptr, _ uintptr, _ error) error {
	if r != 0 {
		return fmt.Errorf("NTSTATUS 0x%08x", uint32(r))
	}
	return nil
}

// ImageBase reads the main executable's load address from the process's PEB (ImageBaseAddress at +0x10).
func (p *Process) ImageBase() (uint64, error) {
	var pbi struct {
		ExitStatus                   uintptr
		PebBaseAddress               uintptr
		AffinityMask                 uintptr
		BasePriority                 uintptr
		UniqueProcessID              uintptr
		InheritedFromUniqueProcessID uintptr
	}
	var ret uint32
	if err := ntStatus(procNtQueryInformationPro.Call(uintptr(p.h), processBasicInformationCls,
		uintptr(unsafe.Pointer(&pbi)), unsafe.Sizeof(pbi), uintptr(unsafe.Pointer(&ret)))); err != nil {
		return 0, fmt.Errorf("query the game process: %w", err)
	}
	b := p.Read(uint64(pbi.PebBaseAddress)+0x10, 8)
	if len(b) != 8 {
		return 0, fmt.Errorf("read the game's image address")
	}
	return binary.LittleEndian.Uint64(b), nil
}

// imageSize reads SizeOfImage from the PE header at base.
func (p *Process) imageSize(base uint64) (uint64, error) {
	dos := p.Read(base, 0x40)
	if len(dos) != 0x40 || dos[0] != 'M' || dos[1] != 'Z' {
		return 0, fmt.Errorf("no PE header at 0x%x", base)
	}
	nt := base + uint64(binary.LittleEndian.Uint32(dos[0x3c:]))
	h := p.Read(nt, 0x58)
	if len(h) != 0x58 || string(h[:4]) != "PE\x00\x00" {
		return 0, fmt.Errorf("no PE signature at 0x%x", nt)
	}
	return uint64(binary.LittleEndian.Uint32(h[0x50:])), nil // OptionalHeader.SizeOfImage
}

// ImageSize reads SizeOfImage from the PE header at base. Read it while the game is still suspended: its protection
// may scrub the header once it has unpacked.
func (p *Process) ImageSize(base uint64) (uint64, error) { return p.imageSize(base) }

// ReadImage reads the whole loaded image (size bytes at base), region by region; pages that can't be read come back
// as zeros.
func (p *Process) ReadImage(base, size uint64) []byte {
	img := make([]byte, size)
	for off := uint64(0); off < size; {
		mbi, ok := p.query(base + off)
		if !ok {
			break
		}
		end := uint64(mbi.BaseAddress) + uint64(mbi.RegionSize) - base
		if end > size {
			end = size
		}
		if end <= off {
			break
		}
		if mbi.State == memCommit && mbi.Protect&(pageNoAccess|pageGuard) == 0 {
			if b := p.Read(base+off, int(end-off)); len(b) > 0 {
				copy(img[off:], b)
			}
		}
		off = end
	}
	return img
}

// firstRegionSize is the size of the region at base: the PE header's page (0x1000) until the game's protection
// has unpacked and remapped the image as one block.
func (p *Process) firstRegionSize(base uint64) uint64 {
	mbi, ok := p.query(base)
	if !ok {
		return 0
	}
	return uint64(mbi.RegionSize)
}
