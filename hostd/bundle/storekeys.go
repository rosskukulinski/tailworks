package bundle

// IsStoreKey reports whether a wrangler config key is a registered platform
// store binding (RegisterStoreKind), i.e. one tailworks supports: the CLI does
// not warn about it (cmd/tw wranglercfg.go).
func IsStoreKey(wranglerKey string) bool {
	storeKindsMu.RLock()
	defer storeKindsMu.RUnlock()
	_, ok := storeKinds[wranglerKey]
	return ok
}
