#!/bin/sh
# release.sh <version> [targets…]: self-contained tw release tarballs.
#
#   dist/tailworks-<version>-<os>-<arch>.tar.gz   (default targets: darwin-arm64 linux-arm64 linux-amd64)
#   dist/SHA256SUMS
#   dist/SHA256SUMS.sig                     (signed builds: TW_RELEASE_SIGNING_KEY, below)
#
# Each tarball holds: bin/{tw,tw-mgmt,tw-hostd} (CGO_ENABLED=0,
# esbuild built into tailworks), bin/workerd (the pinned npm
# @cloudflare/workerd-<os>-<arch>, integrity-checked against
# package-lock.json), share/tailworks/create-tailworks (templates + build.mjs),
# share/tailworks/node_modules (the templates' runtime deps, vendored: deploy works
# offline, no npm/node), share/tailworks/catalog.json (the release catalog,
# internal/catalog), install.sh, VERSION, SHA256SUMS (+ SHA256SUMS.sig), INSTALL.md,
# LICENSE and THIRD_PARTY_NOTICES (+ share/tailworks/vm/{KERNEL,ROOTFS}-SOURCE.md
# with a microVM guest; scripts/third-party-notices.mjs, issue #102).
# linux tarballs also hold bin/tw-vmhelper (the microVM tier's privileged
# helper; `sudo tw host setup --microvm` installs a root-owned copy) and,
# if TW_VM_ASSETS_<arch> names a directory (make vm-assets, in Lima), the
# microVM guest in share/tailworks/vm: vmlinux (+ kernel.json), rootfs.ext4 +
# manifest.json, and Firecracker + jailer, all for that arch.
# darwin tarballs also hold bin/tw-vz (cgo: the microVM VMM on Virtualization.framework,
# signed with the virtualization entitlement) and, if TW_VZ_ASSETS names a
# directory (make vz-assets), its guest in share/tailworks/vm.
# darwin binaries are ad-hoc code-signed (codesign -s -; they are unsigned
# otherwise and arm64 macOS refuses to run unsigned code).
set -eu
VERSION=$1
shift
TARGETS=${*:-darwin-arm64 linux-arm64 linux-amd64}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
DIST=$ROOT/dist
CACHE=$DIST/cache
cd "$ROOT"
export GOTOOLCHAIN=local
WORKERD_VERSION=$(node -p 'require("./package-lock.json").packages["node_modules/workerd"].version')
mkdir -p "$DIST" "$CACHE"
# Every binary carries the release version and commit (internal/version).
COMMIT=$(git rev-parse HEAD)
VPKG=github.com/rosskukulinski/tailworks/internal/version
VLDFLAGS="-X $VPKG.Version=$VERSION -X $VPKG.Commit=$COMMIT"
# The newest mgmt schema migration this release applies (ADR-041 §3).
PROTOCOLS=$(go run ./internal/version/protocols)
SCHEMA=$(ls mgmt/migrations/*.sql 2>/dev/null | sed 's|.*/||; s|\..*||' | LC_ALL=C sort -u | tail -n 1)
# The release catalog (internal/catalog): every release's notes and upgrade facts, for offline plans.
CATALOG=$CACHE/catalog.json
go run ./internal/catalog/dump >"$CATALOG"

# The JS packages must be built (dist/ is gitignored).
for p in tailnet mcp; do
	[ -f "js/packages/$p/dist/index.js" ] || { echo "release: js/packages/$p/dist missing (make js-build)"; exit 1; }
done

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

# Signing (issue #71): with TW_RELEASE_SIGNING_KEY (an Ed25519 private key
# file; release.yml passes the TW_RELEASE_SIGNING_KEY secret), every
# SHA256SUMS (each tarball's and dist/'s) gets SHA256SUMS.sig, checked
# against the pinned keys in internal/releasesig/allowed_signers.
# TW_RELEASE_SIGN=1 makes the key required; without one the build is
# unsigned (install.sh accepts that only for local versions, "x.y.z-…").
if [ -z "${TW_RELEASE_SIGNING_KEY:-}" ]; then
	[ "${TW_RELEASE_SIGN:-0}" != 1 ] || { echo "release: TW_RELEASE_SIGN=1 but no TW_RELEASE_SIGNING_KEY"; exit 1; }
	echo "release: WARNING: unsigned build (no TW_RELEASE_SIGNING_KEY)"
fi
sign() { # file: write file.sig
	[ -n "${TW_RELEASE_SIGNING_KEY:-}" ] || return 0
	rm -f "$1.sig"
	ssh-keygen -q -Y sign -f "$TW_RELEASE_SIGNING_KEY" -n tw-release "$1"
	ssh-keygen -Y verify -f "$ROOT/internal/releasesig/allowed_signers" -I tw-release -n tw-release -s "$1.sig" <"$1" >/dev/null ||
		{ echo "release: $1.sig does not verify against internal/releasesig/allowed_signers (is the signing key pinned there?)"; exit 1; }
}

# vendored node_modules (same for every target: pure JS)
VNM=$CACHE/vendor
rm -rf "$VNM"
# Roots: what the templates need to init + deploy offline (scripts/template-roots.mjs).
ROOTS=$(node scripts/template-roots.mjs js/packages/create-tailworks/templates | tr '\n' ' ')
echo "release: vendoring the templates' deps: $ROOTS"
node scripts/vendor-node-modules.mjs "$ROOT" "$VNM/node_modules" $ROOTS >"$CACHE/vendor.json"
echo "release: vendored node_modules $(cat "$CACHE/vendor.json")"
# The vendored tree is shared by every target, so it must be pure JS: no
# native addons (.node), no ELF/Mach-O/PE executables, no package that
# declares "os"/"cpu" (a platform package). If one ever appears, vendor
# per target instead of shipping another platform's binary.
native=$(node scripts/check-pure-js.mjs "$VNM/node_modules") || { echo "release: vendored node_modules are not pure JS:"; echo "$native"; exit 1; }
echo "release: vendored node_modules are pure JS ($native)"

for t in $TARGETS; do
	os=${t%-*}
	arch=${t#*-}
	name=tailworks-$VERSION-$os-$arch
	st=$DIST/stage/$name
	rm -rf "$st"
	mkdir -p "$st/bin" "$st/share/tailworks"
	echo "release: $name"

	# Go binaries
	# cmd/tw-netd is not shipped: tw-hostd links netd as a library, and the
	# binary is only the e2e-local test harness.
	cmds="./cmd/tw ./cmd/tw-mgmt ./cmd/tw-hostd"
	[ "$os" = linux ] && cmds="$cmds ./cmd/tw-vmhelper"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w $VLDFLAGS" \
		-o "$st/bin/" $cmds
	# tw-vz (darwin): the microVM tier's per-VM VMM links Virtualization.framework,
	# so it is the one cgo binary; built on (and for) this Mac's arch only.
	if [ "$os" = darwin ]; then
		if [ "$(uname -s)" = Darwin ] && [ "$(go env GOARCH)" = "$arch" ]; then
			CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w $VLDFLAGS" -o "$st/bin/tw-vz" ./cmd/tw-vz
		else
			echo "release: $os-$arch: NO tw-vz (needs cgo on a $arch Mac): no microVM tier"
		fi
	fi

	# workerd from the npm platform package, checked against the lockfile.
	# npm names amd64 "64" (@cloudflare/workerd-linux-64).
	npmarch=$arch
	[ "$arch" = amd64 ] && npmarch=64
	pkg=@cloudflare/workerd-$os-$npmarch
	tgz=$CACHE/workerd-$os-$npmarch-$WORKERD_VERSION.tgz
	want=$(node -p "require('./package-lock.json').packages['node_modules/$pkg'].integrity")
	if [ ! -f "$tgz" ]; then
		(cd "$CACHE" && npm pack --silent "$pkg@$WORKERD_VERSION" >/dev/null && mv "cloudflare-workerd-$os-$npmarch-$WORKERD_VERSION.tgz" "$tgz")
	fi
	got="sha512-$(openssl dgst -sha512 -binary "$tgz" | openssl base64 -A)"
	[ "$got" = "$want" ] || { echo "release: $pkg integrity mismatch ($got != $want)"; exit 1; }
	tar -xzf "$tgz" -C "$CACHE" package/bin/workerd
	mv "$CACHE/package/bin/workerd" "$st/bin/workerd"
	rm -rf "$CACHE/package"
	chmod 755 "$st/bin/"*
	# Every binary must be for this target (catches a wrong npm package or GOARCH).
	node scripts/check-arch.mjs "$os" "$arch" "$st/bin/"*

	# templates + bundler sources, and the vendored modules
	mkdir -p "$st/share/tailworks/create-tailworks"
	cp -R js/packages/create-tailworks/package.json js/packages/create-tailworks/bin js/packages/create-tailworks/lib js/packages/create-tailworks/templates "$st/share/tailworks/create-tailworks/"
	find "$st/share/tailworks/create-tailworks/templates" -name node_modules -prune -exec rm -rf {} + 2>/dev/null || true
	find "$st/share/tailworks/create-tailworks/templates" -name dist -type d -prune -exec rm -rf {} + 2>/dev/null || true
	cp -R "$VNM/node_modules" "$st/share/tailworks/node_modules"

	# microVM guest + Firecracker (linux, when built: make vm-assets)
	microvm=false
	eval "va=\${TW_VM_ASSETS_$arch:-}"
	if [ "$os" = linux ] && [ -n "$va" ]; then
		for f in vmlinux rootfs.ext4 manifest.json firecracker jailer; do
			[ -f "$va/$f" ] || { echo "release: $va/$f missing (make vm-assets)"; exit 1; }
		done
		mkdir -p "$st/share/tailworks/vm"
		cp "$va/vmlinux" "$va/rootfs.ext4" "$va/manifest.json" "$va/firecracker" "$va/jailer" "$st/share/tailworks/vm/"
		[ ! -f "$va/kernel.json" ] || cp "$va/kernel.json" "$st/share/tailworks/vm/"
		chmod 644 "$st/share/tailworks/vm/vmlinux" "$st/share/tailworks/vm/rootfs.ext4" "$st/share/tailworks/vm/"*.json
		# The guest must be this tarball's arch (KVM boots native guests only);
		# manifests from before the "arch" field are arm64.
		node scripts/check-arch.mjs linux "$arch" "$st/share/tailworks/vm/firecracker" "$st/share/tailworks/vm/jailer"
		ga=$(node -p "require('$va/manifest.json').arch || 'arm64'")
		[ "$ga" = "$arch" ] || { echo "release: $va/manifest.json is a $ga guest image, not $arch"; exit 1; }
		chmod 755 "$st/share/tailworks/vm/firecracker" "$st/share/tailworks/vm/jailer"
		microvm=true
	fi
	# microVM guest for the vz backend (darwin, when built: make vz-assets): the
	# virtiofs kernel as vmlinux + the same guest image; needs bin/tw-vz.
	if [ "$os" = darwin ] && [ -n "${TW_VZ_ASSETS:-}" ] && [ -f "$st/bin/tw-vz" ]; then
		for f in vmlinux rootfs.ext4 manifest.json kernel-manifest.json; do
			[ -f "$TW_VZ_ASSETS/$f" ] || { echo "release: $TW_VZ_ASSETS/$f missing (make vz-assets)"; exit 1; }
		done
		mkdir -p "$st/share/tailworks/vm"
		cp "$TW_VZ_ASSETS/vmlinux" "$TW_VZ_ASSETS/rootfs.ext4" "$TW_VZ_ASSETS/manifest.json" "$TW_VZ_ASSETS/kernel-manifest.json" "$st/share/tailworks/vm/"
		chmod 644 "$st/share/tailworks/vm/"*
		microvm=true
	fi
	if [ "$os" = darwin ]; then
		if [ $microvm = true ]; then echo "release: $os-$arch microVM guest (vz) in share/tailworks/vm: included"
		else echo "release: $os-$arch microVM guest (vz): NOT included (make vz-assets needs Lima tw-dev)"; fi
	fi
	if [ "$os" = linux ]; then
		if [ $microvm = true ]; then echo "release: $os-$arch microVM guest assets (share/tailworks/vm): included"
		else echo "release: $os-$arch microVM guest assets (share/tailworks/vm): NOT included (the microVM tier needs a release built with them: make release with Lima tw-dev running, or on Linux)"; fi
	fi

	cp scripts/install.sh "$st/install.sh"
	chmod 755 "$st/install.sh"
	cp docs/INSTALL.md "$st/INSTALL.md"
	printf '%s\n' "$VERSION" >"$st/VERSION"
	cp "$CATALOG" "$st/share/tailworks/catalog.json"
	cat >"$st/share/tailworks/release.json" <<EOF
{"version":"$VERSION","os":"$os","arch":"$arch","workerd":"$WORKERD_VERSION","esbuild":"built into tw","sandbox":"$( [ "$os" = darwin ] && echo seatbelt || echo landlock )","microvm":$microvm,"commit":"$COMMIT","schema":"$SCHEMA","protocols":$PROTOCOLS,"built":"$(date -u +%Y-%m-%dT%H:%M:%SZ)"}
EOF
	# LICENSE, THIRD_PARTY_NOTICES and the guest's source offers (issue #102).
	node scripts/third-party-notices.mjs "$st"

	if [ "$os" = darwin ]; then
		if [ "$(uname -s)" != Darwin ]; then
			echo "release: darwin binaries must be ad-hoc signed on macOS (codesign)"; exit 1
		fi
		for b in "$st/bin/"*; do
			codesign --force --sign - "$b"
			codesign --verify "$b"
		done
		# tw-vz must carry the virtualization entitlement (ad hoc is enough).
		if [ -f "$st/bin/tw-vz" ]; then
			codesign --force --sign - --entitlements cmd/tw-vz/tw-vz.entitlements "$st/bin/tw-vz"
			codesign -d --entitlements - "$st/bin/tw-vz" 2>/dev/null | grep -q com.apple.security.virtualization ||
				{ echo "release: tw-vz lacks the virtualization entitlement"; exit 1; }
			# The probe configures a VM, so it needs a Mac that can run one.
			# GitHub's macOS runners are VMs without nested virtualization:
			# there it reports "not available on this hardware" (VZErrorDomain
			# Code=2), which says nothing about the tarball, so warn and go on.
			# Any other failure (bad kernel, bad entitlement) still fails.
			if [ -f "$st/share/tailworks/vm/vmlinux" ]; then
				if ! out=$("$st/bin/tw-vz" -check "$st/share/tailworks/vm/vmlinux" 2>&1); then
					case "$out" in
					*"not available on this hardware"*)
						echo "release: WARNING: tw-vz -check skipped: this machine can't run VMs ($(printf %s "$out" | head -n1))" ;;
					*) printf '%s\n' "$out"; exit 1 ;;
					esac
				else
					printf '%s\n' "$out"
				fi
			fi
		fi
		# workerd must run here (the native target).
		if [ "$(uname -m)" = "$arch" ]; then
			"$st/bin/workerd" --version
			"$st/bin/tw" version
		fi
	fi
	xattr -cr "$st" 2>/dev/null || true

	(cd "$st" && find . -type f ! -name SHA256SUMS | LC_ALL=C sort | sed 's|^\./||' | while IFS= read -r f; do sha256 "$f"; done >SHA256SUMS)
	sign "$st/SHA256SUMS"
	COPYFILE_DISABLE=1 tar -czf "$DIST/$name.tar.gz" -C "$DIST/stage" "$name"
	sh scripts/check-release-notices.sh "$DIST/$name.tar.gz"
	ls -lh "$DIST/$name.tar.gz" | awk '{print "release: " $NF " " $5}'
done
(cd "$DIST" && sha256 tailworks-"$VERSION"-*.tar.gz >SHA256SUMS && cat SHA256SUMS)
sign "$DIST/SHA256SUMS"
