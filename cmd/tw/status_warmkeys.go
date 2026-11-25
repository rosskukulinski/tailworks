package main

import "fmt"

// warmKeysLabel is `tw status`'s count of a tenant's pooled preauthorized
// join keys in mgmt custody (#158): whoever reads custody can join a node
// with one until it expires. `tw down` revokes them.
func warmKeysLabel(n int) string {
	if n == 1 {
		return "1 warm key in custody"
	}
	return fmt.Sprintf("%d warm keys in custody", n)
}
