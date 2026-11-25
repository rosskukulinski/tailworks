//go:build !linux

// tw-guestd is the init of a tailworks microVM guest; it only builds for Linux.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tw-guestd: Linux guests only")
	os.Exit(2)
}
