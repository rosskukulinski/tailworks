package hostd_test

import "testing"

// assertNoCfGeo checks a pubprobe response (who(): cfKeys, cfGeo): tailworks
// sets none of Cloudflare's request.cf geolocation fields (country, colo,
// latitude, ...), for tailnet or public requests, so they must be absent
// (undefined), never "" or 0, and ported code can feature-detect them
// (#302, docs/COMPAT.md#fetch).
func assertNoCfGeo(t *testing.T, where string, r map[string]any) {
	t.Helper()
	keys, ok := r["cfKeys"].([]any)
	if !ok || len(keys) == 0 {
		t.Fatalf("%s: no request.cf keys reported: %s", where, js(r))
	}
	if geo, _ := r["cfGeo"].([]any); len(geo) != 0 {
		t.Errorf("%s: request.cf has geo fields %v (want none; keys %v)", where, geo, keys)
	}
	t.Logf("%s: request.cf keys %v", where, keys)
}
