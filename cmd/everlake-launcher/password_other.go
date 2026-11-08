//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// readPassword asks for a password without showing it (stty), when stdin is a terminal.
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	off := exec.Command("stty", "-echo")
	off.Stdin = os.Stdin
	if off.Run() == nil {
		defer func() {
			on := exec.Command("stty", "echo")
			on.Stdin = os.Stdin
			on.Run()
			fmt.Println()
		}()
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
