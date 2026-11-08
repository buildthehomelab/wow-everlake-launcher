//go:build windows

package main

import (
	"os"
	"syscall"
)

var procAttachConsole = syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")

// attachConsole gives a command typed in a terminal somewhere to print. The Windows launcher is built as a GUI
// program (-H=windowsgui) so a double-click opens no console window; such a program starts without standard
// handles, so here it borrows the terminal it was started from. Handles that are already set (output redirected to
// a file or a pipe) are kept.
//
// A terminal doesn't wait for a GUI program to finish: the prompt comes back at once and the output arrives after
// it. The console build (everlake-launcher-cli.exe) behaves like any command-line tool.
func attachConsole() {
	if valid(os.Stdout) && valid(os.Stderr) && valid(os.Stdin) {
		return
	}
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return // not started from a terminal, or already has a console
	}
	if !valid(os.Stdout) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stdout = f
		}
	}
	if !valid(os.Stderr) {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			os.Stderr = f
		}
	}
	if !valid(os.Stdin) {
		if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = f
		}
	}
}

func valid(f *os.File) bool {
	if f == nil {
		return false
	}
	h := syscall.Handle(f.Fd())
	if h == 0 || h == syscall.InvalidHandle {
		return false
	}
	t, err := syscall.GetFileType(h)
	return err == nil && t != 0 // FILE_TYPE_UNKNOWN: no real handle behind it
}
