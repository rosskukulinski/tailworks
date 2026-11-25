//go:build !linux

// tw-vmhelper is Linux-only (the microVM tier needs KVM).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tw-vmhelper: Linux only")
	os.Exit(1)
}
