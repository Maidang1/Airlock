//go:build !unix

package main

import "context"

func watchResize(context.Context, int, func(uint16, uint16)) func() {
	return func() {}
}
