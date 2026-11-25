//go:build !darwin

// tw-vz is the macOS (Virtualization.framework) microVM VMM; on Linux the
// microVM tier uses Firecracker (hostd/vm).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tw-vz: macOS only (Virtualization.framework)")
	os.Exit(1)
}
