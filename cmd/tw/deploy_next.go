package main

import "fmt"

// printDeployNext is what to do right after a deploy: call it, watch it.
// The caller grant is the usual reason the curl hangs, so it is named too.
func printDeployNext(name, url string) {
	fmt.Printf("\n  try it   curl %s   (or open it in a browser on your tailnet)\n", url)
	fmt.Printf("  watch    tw logs %s -f\n", name)
	fmt.Printf("  no answer? the caller needs a grant to the app's tag: tw policy prints it\n")
}
