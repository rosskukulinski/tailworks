package docs

import (
	"path"
	"strings"
)

// GitHubBlob is the URL prefix of a repo file on GitHub's master branch.
const GitHubBlob = "https://github.com/rosskukulinski/tailworks/blob/master/"

// OnGitHubOnly returns the GitHub URL of a ref under contracts/ or design/,
// which the CLI doesn't embed (issue #479), or "" for any other ref.
func OnGitHubOnly(ref string) string {
	doc := strings.TrimPrefix(path.Clean(strings.TrimPrefix(ref, "./")), "docs/")
	if !strings.HasPrefix(doc, "contracts/") && !strings.HasPrefix(doc, "design/") {
		return ""
	}
	return GitHubBlob + RepoPath(doc)
}
