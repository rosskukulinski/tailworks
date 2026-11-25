#!/usr/bin/env bash
# rename-tailworks.sh: the scripted part of the twd → tailworks rename (issue #86).
#
# Run once from the repo root on a clean tree:
#
#   scripts/rename-tailworks.sh
#
# It (1) `git mv`s every path containing "twd" and (2) rewrites file
# contents. Hand fixes, `make api`, `tw types` and the golden files come
# after, in their own commits. Kept for the record; the rename is one-shot
# and the script is not idempotent (a second run finds nothing to do).
#
# The rule (issue #86): the long name `tailworks` goes on what people
# search for or install (repo, Go module, npm packages, install dirs, the
# per-app config file, prose); the short name `tw` goes everywhere else
# (the CLI, daemons, env vars, tags, headers, metrics, generated JS).
#
# Not rewritten (history): changes/*.md and changes/released/ (except
# changes/README.md), status/*.md (except status/README.md), STATUS.md
# (frozen), CHANGELOG.md and the release catalog built from it
# (internal/catalog/releases/), and the mgmt migrations (append-only;
# only comments mention twd). Replacements are case-sensitive (twd, Twd,
# TWD), so base64 like the "…ytWd/…" in package-lock.json is untouched.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Peer capabilities move from twd.dev/cap/* (a domain we don't own) to a
# namespace we clearly own. Policy files carry this string, so it is chosen once.
CAP_NS=github.com/rosskukulinski/tailworks/cap

skip() { # history files that keep saying twd
	case "$1" in
	changes/README.md | status/README.md) return 1 ;;
	changes/* | status/* | STATUS.md | CHANGELOG.md | internal/catalog/releases/* | mgmt/migrations/*.sql | scripts/rename-tailworks.sh) return 0 ;;
	esac
	return 1
}

export CAP_NS
# The replacement, shared by paths and contents. -0777 slurps the file.
# Each file runs: long-name passes, then the prose pass (Markdown outside
# code, and comment lines in code), then the generic twd → tw pass.
cat >/tmp/rename-tailworks.pl <<'PERL'
use strict; use warnings;
our $CAP = $ENV{CAP_NS};
our $IS_MD = 0;     # Markdown: prose outside code spans/blocks
our $IS_CODE = 0;   # source with comment lines worth treating as prose
# Subcommands of `tw`: "twd <one of these>" is the command, not the project.
our $CMDS = join '|', qw(up down d1 r2 link whoami status tenant init dev types deploy policy
	deployments previews promote rollback ls undeploy expose secret kv queues telemetry
	kill approvals approve notify audit logs doctor docs completion mcp agent host image
	upgrade release version help);

sub long {
	local $_ = shift;
	s/TWD_CREATE_TWD/TW_CREATE_TAILWORKS/g;
	# repo / Go module
	s{rosskukulinski/twd\b}{rosskukulinski/tailworks}g;
	s{<org>/twd\b}{<org>/tailworks}g;
	# twd.dev is not ours: error docs go to docs/errors.md, caps to $CAP
	s{https://twd\.dev/errors/([\w-]+)}{https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#$1}g;
	s{twd\.dev/cap/}{$CAP/}g;
	s{twd\.dev\.jsonc}{tailworks.dev.jsonc}g;
	s{https://twd\.dev\b}{https://github.com/rosskukulinski/tailworks}g;
	# npm
	s{\@twd/}{\@tailworks/}g;
	s{\@twd\b}{\@tailworks}g;
	s{create-twd}{create-tailworks}g;
	s{\bnpm (create|init) twd\b}{npm $1 tailworks}g;
	# install root and system dirs
	s{(?<![\w.?])\.twd\b}{.tailworks}g; # not req.cf?.twd
	s{\b(share|etc|libexec|run|opt)/twd\b}{$1/tailworks}g;
	s{\.config/twd\b}{.config/tailworks}g;
	s{/var/lib/twd-vm\b}{/var/lib/tailworks-vm}g;
	s{"(share|etc|libexec)",(\s*)"twd"}{"$1",$2"tailworks"}g;
	s{RuntimeDirectory=twd\b}{RuntimeDirectory=tailworks}g;
	s{~/Downloads/twd\b}{~/Downloads/tailworks}g;
	s{/tmp/twd\b(?!-)}{/tmp/tailworks}g;
	# the per-app config file, its docs page and schema; ~/.tw*/twd.json is
	# internal state and gets the short name
	s{(?<![\w.-])twd\.jsonc}{tailworks.jsonc}g;
	s{(?<![\w.-])twd-jsonc\b}{tailworks-jsonc}g;
	s{(?<![\w.-])twd\.schema\.json}{tailworks.schema.json}g;
	s{(?<![\w.-])twd\.json\b}{tw.json}g;
	# the core agent skill's frontmatter
	s{^(name:\s*)twd$}{${1}tw}mg;
	# launchd label (reverse DNS)
	s{\bdev\.twd\.}{dev.tailworks.}g;
	# release tarballs: tailworks-<ver>-<os>-<arch>.tar.gz
	s{\btwd-(?=<ver|\$\(RELEASE_VERSION|\$\{?VER|\$v\b|%s-%s|v?\d+\.\d+\.\d+-|\*-(?:darwin|linux|\$)|\*\.tar)}{tailworks-}g;
	s{(dist|/t|~/\.tailworks-e2e|tailworks)/twd-\*}{$1/tailworks-*}g;
	s{/twd-\*/install\.sh}{/tailworks-*/install.sh}g;
	s{"twd-\*"}{"tailworks-*"}g;
	s{sha256 twd-"\$VERSION"}{sha256 tailworks-"\$VERSION"}g;
	s{\btwd-%s-<os>}{tailworks-%s-<os>}g;
	s{\bn=twd-\$2-}{n=tailworks-\$2-}g;
	s{\bThe twd Authors\b}{The tailworks Authors}g;
	s{\btwd-monorepo\b}{tailworks-monorepo}g;
	# header prefixes written without their dash must not become "x-tw",
	# which would also match x-twilio-*
	s{(["'`])x-twd\1}{$1x-tw-$1}g;
	s{x-twd\*}{x-tw-*}g;
	return $_;
}

sub prose { # one run of prose text
	local $_ = shift;
	s{\btwd's\b}{tailworks'}g;
	s{\bTwd\b(?![\w-])}{Tailworks}g;
	# after an article or determiner it is the project, whatever follows
	s{\b(the|The|one|per|a|an|each|every|this|that|any|your|its|of) twd\b(?![\w"'/=-])(?!\.\w)(?!\s+(?:up|dev|deploy|init|link)\b)}{$1 tailworks}g;
	# standalone "twd": not part of a path, flag, tag, identifier or hyphenated name
	s{(?<![\w./\@:\$~"'`<>=\{-])twd(?![\w"'/=-])(?!\.\w)(?!:\w)(?!\s+(?:$CMDS)\b)(?!\s+[-<\[])}{tailworks}g;
	s{(?<=[.!?]\s)tailworks\b(?![.\w-])}{Tailworks}g;
	return $_;
}

sub md_line { # split on `code` spans (which may wrap lines); prose outside them
	my $l = shift;
	my @p = split /(`+[^`]*`+)/, $l;
	for (@p) { $_ = prose($_) unless /^`/ }
	return join '', @p;
}

sub generic {
	local $_ = shift;
	s/TWD/TW/g; s/Twd/Tw/g; s/(?<!Ge)twd/tw/g; # not os.Getwd
	return $_;
}

sub rewrite {
	my $s = long(shift);
	if ($IS_MD) {
		my $fence = 0; my @out; my $para = '';
		for my $l (split /(?<=\n)/, $s) {
			if ($l =~ /^\s*(```|~~~)/) {
				push @out, md_line($para), $l; $para = ''; $fence = !$fence; next;
			}
			if ($fence) { push @out, $l } else { $para .= $l }
		}
		push @out, md_line($para);
		$s = join '', @out;
	} elsif ($IS_CODE) {
		# comment-only lines: //, #, * (block comment bodies)
		$s =~ s{^(\s*(?://+|#+|\*)\s?)(.*)$}{$1 . md_line($2)}gme;
	}
	return generic($s);
}
1;
PERL

# 1. paths. git mv every tracked path containing twd (outside history).
git ls-files | grep -E 'twd' | while IFS= read -r f; do
	skip "$f" && continue
	n=$(perl -e 'require "/tmp/rename-tailworks.pl"; print main::generic(main::long($ARGV[0]))' "$f")
	[ "$n" = "$f" ] && continue
	mkdir -p "$(dirname "$n")"
	git mv "$f" "$n"
done

# 2. contents.
git grep -lI -e twd -e Twd -e TWD | while IFS= read -r f; do
	skip "$f" && continue
	md=0 code=0
	case "$f" in
	*.md | api/openapi.yaml | *.json | *.html | *.txt | *.css) md=1 ;;
	*.go | *.ts | *.js | *.mjs | *.cjs | *.sh | *.bash | *.yml | *.yaml | Makefile | *.mk | *.toml | *.jsonc | *.sql | *.py | scripts/pre-commit | .gitignore) code=1 ;;
	esac
	IS_MD=$md IS_CODE=$code perl -0777 -i -e '
		require "/tmp/rename-tailworks.pl";
		$main::IS_MD = $ENV{IS_MD}; $main::IS_CODE = $ENV{IS_CODE};
		local $/; my $s = <>; print main::rewrite($s);
	' "$f"
done

# 3. names the generic pass leaves still matching /twd/i (twddocs → twdocs, …).
git grep -lE "twdocs|createTwDir|TestTwDevWrangler" -- ":!scripts/rename-tailworks.sh" | xargs perl -pi -e 's/\btwdocs\b/repodocs/g; s/\bcreateTwDir\b/createTailworksDir/g; s/\bTestTwDevWrangler\b/TestDevWrangler/g'

# 4. user-facing strings in code: the generic pass made "not supported on twd"
# into "… on tw"; where tw means the project, not the command, say tailworks.
# Comment lines were already handled by the prose pass.
cat >/tmp/rename-tailworks-strings.pl <<'PERL'
our $CMDS = 'up|down|d1|r2|link|whoami|status|tenant|init|dev|types|deploy|policy|deployments|previews|promote|rollback|ls|undeploy|expose|secret|kv|queues|telemetry|kill|approvals|approve|notify|audit|logs|doctor|docs|completion|mcp|agent|host|image|upgrade|release|version|help';
our $NB = qr{(?![\w.\-/])(?!\s+(?:$CMDS)\b)(?!\s+(?:binary|CLI|command|commands)\b)};
sub fix {
	local $_ = shift;
	return $_ if m{^\s*(?://|#|\*)};
	s{(?<![\w\$./:-])tw's\b}{tailworks'}g;
	s{\b(on|by|from|in|of|than|to|for|inside|under|with) tw\b(?=[?)"'`:;,.\s]|$)$NB}{$1 tailworks}g;
	s{\b(a|an|the|this|that|another|newer|older|old|every|each|one|per|your|its|dedicated) tw\b$NB}{$1 tailworks}g;
	s{(?<![\w\$.`'"/:-])tw (?=(?:can|can't|cannot|fails|hands|treats|runs|never|is|has|doesn't|didn't|does|won't|serves|installs|grants|only|no longer|keeps|ships|sets|would|must|stops|checks|knows|needs|as)\b)}{tailworks }g;
	s{\b(start|upgrade|reinstall|Start|Upgrade) tw\b(?=[\s:(]|$)$NB}{$1 tailworks}g;
	return $_;
}
1;
PERL
git diff --name-only --diff-filter=AMR "$(git merge-base HEAD origin/master)" -- '*.go' '*.ts' '*.js' '*.mjs' '*.sh' '*.yml' Makefile |
	grep -v '^scripts/rename-tailworks.sh$' | while IFS= read -r f; do
	perl -i -ne 'BEGIN { require "/tmp/rename-tailworks-strings.pl" } print fix($_)' "$f"
done
rm -f /tmp/rename-tailworks-strings.pl

rm -f /tmp/rename-tailworks.pl
echo "rename-tailworks: done. Leftovers:"
git grep -cI -i twd | awk -F: '{n+=$2} END {print n+0, "occurrences"}'
