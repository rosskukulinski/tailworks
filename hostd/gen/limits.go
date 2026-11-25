package gen

import "fmt"

// v8Flags is the config's v8Flags line for o ("" for none). HeapMiB becomes
// --max-old-space-size, which V8 applies to every isolate the workerd
// creates (the Worker and each Durable Object class). An isolate that
// reaches it can't allocate: V8 aborts the whole workerd ("Reached heap
// limit"), and hostd restarts it like any other crash (issue #115; see
// docs/limits.md "Process tier").
func v8Flags(o Options) string {
	if o.HeapMiB <= 0 {
		return ""
	}
	return fmt.Sprintf("  v8Flags = [ %s ],\n", cq(fmt.Sprintf("--max-old-space-size=%d", o.HeapMiB)))
}
