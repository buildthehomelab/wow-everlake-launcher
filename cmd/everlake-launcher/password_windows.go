//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	procGetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleMode")
	procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")
)

const enableEchoInput = 0x0004

// readPassword asks for a password without showing it, when stdin is a console.
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	h := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		procSetConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput))
		defer func() {
			procSetConsoleMode.Call(uintptr(h), uintptr(mode))
			fmt.Println()
		}()
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
