//go:build !linux

package main

import "errors"

func runSFTPBridge(string) error {
	return errors.New("airlock-init sftp-bridge is supported on Linux guests only")
}

func runInit() error {
	return errors.New("airlock-init is supported on Linux guests only")
}
