//go:build !linux

package main

import (
	"context"
	"errors"
	"net"
)

// HandlePTY is supported on Linux guests only.
func HandlePTY(context.Context, net.Conn, string) error {
	return errors.New("PTY handler requires Linux")
}
