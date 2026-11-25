package main

import "github.com/rosskukulinski/tailworks/internal/emailstub"

// emailImportFailure is the CLI's refusal when the built-in bundler found
// the app's own code importing cloudflare:email (internal/emailstub,
// refuse.go), or nil when err is some other build error.
func emailImportFailure(err error) error {
	ie, ok := emailstub.AsImportError(err)
	if !ok {
		return nil
	}
	return fail(emailstub.Code, ie.Error()).
		withFix("email is not supported on tailworks yet: remove the cloudflare:email import, or keep it out of the tailworks build (for example behind a module only your Cloudflare build imports)",
			"see docs/COMPAT.md#email")
}
