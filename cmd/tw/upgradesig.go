package main

import (
	"crypto/ed25519"
	"errors"
	"os"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
)

// tw upgrade on a tw up machine checks the release's signature before
// anything in it runs (issue #98), with the verifier tw-hostd's
// self-upgrade uses (hostd/upgradesig.go): SHA256SUMS.sig must be a
// signature of the tarball's SHA256SUMS by a pinned release key, and every
// file must match it. The SHA256SUMS next to a download comes from the same
// place as the tarball, so it only proves the download is intact.

// releaseSigners is the keys a release must be signed by: the ones pinned
// in this binary, or the allowed_signers file TW_RELEASE_SIGNERS names (as
// for scripts/get.sh and install.sh).
func releaseSigners() ([]ed25519.PublicKey, error) {
	p := os.Getenv("TW_RELEASE_SIGNERS")
	if p == "" {
		return releasesig.Pinned()
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return releasesig.ParseAllowedSigners(b)
}

// allowUnsignedEnv is TW_ALLOW_UNSIGNED=1, the environment form of
// --allow-unsigned (as for scripts/get.sh and install.sh).
func allowUnsignedEnv() bool { return os.Getenv("TW_ALLOW_UNSIGNED") == "1" }

// verifyUpgradeRelease checks the extracted release dir. With
// allowUnsigned, a release that doesn't verify is let through and the
// returned warning says why.
func verifyUpgradeRelease(dir string, allowUnsigned bool) (warning string, err error) {
	keys, err := releaseSigners()
	if err == nil {
		err = releasesig.VerifyTree(dir, keys)
	}
	if err == nil {
		return "", nil
	}
	if allowUnsigned {
		return "the release signature was not checked (--allow-unsigned): " + err.Error(), nil
	}
	if errors.Is(err, releasesig.ErrUnsigned) {
		return "", fail("release_unsigned", "refusing an unsigned release: "+err.Error()).
			withFix("upgrade to a published release: tw upgrade [<version>]",
				"or, for a build you made yourself: tw upgrade --from <tarball|dir> --allow-unsigned")
	}
	e := fail("release_bad_signature", "refusing a release whose signature doesn't verify: "+err.Error())
	if errors.Is(err, releasesig.ErrNoKeys) {
		e.Lines = append(e.Lines, "this tw build pins no release signing key, so it can't verify any release")
	}
	return "", e.withFix("download the release again from GitHub: tw upgrade [<version>]",
		"or, for a build you made yourself: tw upgrade --from <tarball|dir> --allow-unsigned")
}
