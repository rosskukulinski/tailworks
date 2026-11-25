package tsapi

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecretFileModeAndSymlink (L1/L2): a credential file is read only if
// it is a regular 0600 file reached without following a symlink, and the
// mode is that of the file actually opened (fstat).
func TestSecretFileModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "api.env")
	if err := os.WriteFile(good, []byte("TS_API_KEY=tskey-api-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := LoadSecretEnvFile(good, "TS_API_KEY"); err != nil || k != "tskey-api-abc" {
		t.Fatalf("0600: %q %v", k, err)
	}
	loose := filepath.Join(dir, "loose.env")
	os.WriteFile(loose, []byte("TS_API_KEY=x\n"), 0o644)
	if _, err := LoadSecretEnvFile(loose, "TS_API_KEY"); !IsInsecureFile(err) {
		t.Fatalf("0644 accepted: %v", err)
	}
	// LoadEnvFile (non-secret use) still reads it; the secret loader is the strict one.
	if k, err := LoadEnvFile(loose, "TS_API_KEY"); err != nil || k != "x" {
		t.Fatalf("LoadEnvFile 0644: %q %v", k, err)
	}
	link := filepath.Join(dir, "link.env")
	if err := os.Symlink(good, link); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := LoadSecretEnvFile(link, "TS_API_KEY"); err == nil {
		t.Fatal("symlink to a 0600 file was followed")
	}
	oauth := filepath.Join(dir, "oauth.env")
	os.WriteFile(oauth, []byte("TS_OAUTH_CLIENT_SECRET=tskey-client-kAbC123-xyz\n"), 0o600)
	if c, err := LoadOAuthFile(oauth); err != nil || c.ClientID != "kAbC123" {
		t.Fatalf("oauth 0600: %+v %v", c, err)
	}
	olink := filepath.Join(dir, "oauth-link.env")
	os.Symlink(oauth, olink)
	if _, err := LoadOAuthFile(olink); err == nil {
		t.Fatal("oauth symlink followed")
	}
	os.Chmod(oauth, 0o640)
	if _, err := LoadOAuthFile(oauth); !IsInsecureFile(err) {
		t.Fatalf("oauth 0640 accepted: %v", err)
	}
}
