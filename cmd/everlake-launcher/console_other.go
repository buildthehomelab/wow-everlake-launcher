//go:build !windows

package main

// attachConsole is only needed by the Windows GUI build.
func attachConsole() {}
