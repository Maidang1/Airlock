// Command airlock-init runs as PID 1 inside Airlock's lightweight Linux MicroVM
// (and as an SFTP vsock bridge subprocess when invoked with "sftp-bridge").
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "sftp-bridge" {
		if err := runSFTPBridge(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "airlock-init sftp-bridge: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := runInit(); err != nil {
		fmt.Fprintf(os.Stderr, "airlock-init: %v\n", err)
		os.Exit(1)
	}
}
