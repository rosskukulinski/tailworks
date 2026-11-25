package mgmt

// Removed mint modes (ADR-042). Tailworks used to mint with a
// Tailscale API key ("apikey") or through workload identity federation
// ("wif"). Both are gone: a Tailscale tenant mints with an OAuth client,
// a Headscale tenant with its Headscale API key. mgmt refuses to create a
// tenant in a removed mode or switch one to it; a tenant an older tailworks
// stored in one keeps its row (so nothing is lost) but can't mint until an
// admin switches it to oauth.
const (
	mintRemovedAPIKey = "apikey"
	mintRemovedWIF    = "wif"
)

func removedMintMode(mode string) bool {
	return mode == mintRemovedAPIKey || mode == mintRemovedWIF
}

// errMintModeRemoved is the refusal for a tenant stored in a removed mode.
func errMintModeRemoved(t Tenant) *Error {
	return errf(400, "mint_mode_removed",
		"tenant %s is in mint mode %q, which tailworks no longer supports; switch it to an OAuth client: on the host, `tw tenant set %s --oauth-client-file <file>` (or PATCH /v1/tenants/%s {\"mintMode\": \"oauth\", \"oauthClientFile\": \"<file>\"})",
		t.ID, t.MintMode, t.Tailnet, t.ID)
}

// errSetRemovedMintMode refuses a create or update that names a removed
// mode.
func errSetRemovedMintMode(mode string) *Error {
	return errf(400, "mint_mode_removed",
		"mint mode %q was removed; a Tailscale tenant mints with an OAuth client (mintMode oauth, oauthClientFile) and a Headscale tenant with mintMode headscale", mode)
}
