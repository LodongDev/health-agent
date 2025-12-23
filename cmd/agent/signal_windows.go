//go:build windows

package main

import (
	"os"
)

func setupReloadSignal(ch chan<- os.Signal) {
	// SIGHUP is not available on Windows
	// Config reload via signal is not supported
}
