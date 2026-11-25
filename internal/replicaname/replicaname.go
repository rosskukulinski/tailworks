// Package replicaname is the naming of a workload's replicas
// (tailworks.jsonc "replicas"). Replica i ≥ 2 of workload w is deployed as
// a workload of its own, named "w--r<i>".
//
// mgmt names replicas with Name, and refuses any other workload a name
// that Group recognises. hostd has only a workload's name to go by when it
// finds the workload whose R2 blobs a replica shares; it asks Group too.
// One definition serves both, so they can't disagree about which names
// are replicas (#348).
package replicaname

import (
	"strconv"
	"strings"
)

// Sep separates a workload's name from a replica's number.
const Sep = "--r"

// Name is the workload name of replica i (0-based) of workload: the
// workload itself for i == 0, "<workload>--r<i+1>" after that.
func Name(workload string, i int) string {
	if i == 0 {
		return workload
	}
	return workload + Sep + strconv.Itoa(i+1)
}

// Group reports whether name has the shape of a replica's name,
// "<group>--r<n>": a non-empty group, then n ≥ 2 in decimal with no
// leading zero. It returns the group, the workload such a replica belongs
// to. The n is not bounded: mgmt deploys at most 16 replicas, but every
// such name is reserved.
func Group(name string) (group string, ok bool) {
	i := strings.LastIndex(name, Sep)
	if i <= 0 {
		return "", false
	}
	n := name[i+len(Sep):]
	if n == "" || n == "1" || n[0] == '0' {
		return "", false
	}
	for j := 0; j < len(n); j++ {
		if n[j] < '0' || n[j] > '9' {
			return "", false
		}
	}
	return name[:i], true
}
