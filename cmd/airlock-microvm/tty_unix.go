//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

func watchResize(ctx context.Context, fd int, onResize func(cols, rows uint16)) func() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ch:
				if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
					onResize(uint16(w), uint16(h))
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
