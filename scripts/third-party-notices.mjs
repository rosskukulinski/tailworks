// third-party-notices.mjs <stage>: the license files of one release tarball
// (issue #102). scripts/release.sh runs it on each staged tarball directory,
// after share/tailworks/release.json is written and before SHA256SUMS. It writes
//
//   <stage>/LICENSE                the project's own license (BSD-3-Clause
//                                  clause 2: binary distributions reproduce it)
//   <stage>/THIRD_PARTY_NOTICES    every third-party component in the tarball,
//                                  its license and the license text (or where
//                                  the text is in the tarball):
//                                  workerd, the Go modules compiled into bin/,
//                                  the vendored node_modules, and the microVM
//                                  guest when share/tailworks/vm is there
//   <stage>/share/tailworks/vm/KERNEL-SOURCE.md   (with a guest) the GPL
//                                  kernel's corresponding source + written offer
//   <stage>/share/tailworks/vm/ROOTFS-SOURCE.md   (with a guest) the Ubuntu
//                                  packages in rootfs.ext4 (manifest.json
//                                  "packages", guest/rootfs-packages.sh), their
//                                  sources + written offer
//
// Texts that upstream artifacts don't carry (workerd's npm package has no
// LICENSE; Firecracker's release tarball no NOTICE) are vendored in
// third_party/licenses/. Fails (exit 1) when a Go module has no license file
// or a guest's manifest has no package list; a vendored npm package without a
// license file is a warning. scripts/check-release-notices.sh checks the
// tarballs afterwards.
//
// This is a best-effort compliance aid, not legal advice: docs/RELEASING.md,
// "Licenses and notices", lists what a maintainer should review.
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const REPO = "https://github.com/rosskukulinski/tailworks";
const stage = process.argv[2];
if (!stage) {
  console.error("usage: third-party-notices.mjs <stage>");
  process.exit(2);
}
const rel = JSON.parse(fs.readFileSync(path.join(stage, "share/tailworks/release.json"), "utf8"));
const errors = [];
const warnings = [];
const read = (p) => fs.readFileSync(p, "utf8");
const vendored = (p) => read(path.join(ROOT, "third_party/licenses", p));
const atCommit = (p) => `${REPO}/blob/${rel.commit}/${p}`;
const rule = "=".repeat(78);
const sub = "-".repeat(78);

// LICENSE_RE: the files that carry a license or notice in a module/package.
const LICENSE_RE = /^(LICEN[CS]E|COPYING|NOTICE|PATENTS|UNLICENSE)([.\-_].*)?$/i;
const licenseFiles = (dir) =>
  fs.existsSync(dir)
    ? fs.readdirSync(dir, { withFileTypes: true }).filter((e) => e.isFile() && LICENSE_RE.test(e.name)).map((e) => e.name).sort()
    : [];

// inputs.sh pins (Firecracker version, kernel config URL): same commit as the build.
const inputs = Object.fromEntries(
  [...read(path.join(ROOT, "guest/inputs.sh")).matchAll(/^([A-Z_0-9]+)=(\S+)$/gm)].map((m) => [m[1], m[2]]),
);
const expand = (s) => s.replace(/\$\{?([A-Z_0-9]+)\}?/g, (_, k) => inputs[k] ?? "");

// ---- Go modules -----------------------------------------------------------
function goModules() {
  const mods = new Map(); // path@version -> Set(binary)
  const add = (m, who) => (mods.get(m) ?? mods.set(m, new Set()).get(m)).add(who);
  let goVersion = "";
  const bin = path.join(stage, "bin");
  for (const b of fs.readdirSync(bin).sort()) {
    let out;
    try {
      out = execFileSync("go", ["version", "-m", path.join(bin, b)], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] });
    } catch {
      continue; // not a Go binary (workerd)
    }
    const lines = out.split("\n");
    goVersion ||= (lines[0].match(/: (go\S+)/) || [])[1] || "";
    let last = null;
    for (const l of lines) {
      const f = l.split("\t");
      if (f[1] === "dep") {
        last = `${f[2]}@${f[3]}`;
        add(last, `bin/${b}`);
      } else if (f[1] === "=>" && last) {
        // A replace: the code compiled in is the replacement's.
        mods.get(last).delete(`bin/${b}`);
        if (!mods.get(last).size) mods.delete(last);
        if (f[3]) add(`${f[2]}@${f[3]}`, `bin/${b}`);
        else errors.push(`${last} is replaced by a local directory (${f[2]}); its license can't be located`);
      }
    }
  }
  if (fs.existsSync(path.join(stage, "share/tailworks/vm/rootfs.ext4"))) {
    // tw-guestd, inside rootfs.ext4 (built from this commit by guest/build-image.sh).
    const out = execFileSync(
      "go",
      ["list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{with .Replace}}{{.Path}}@{{.Version}}{{else}}{{.Path}}@{{.Version}}{{end}}{{end}}{{end}}", "./guest/tw-guestd"],
      { cwd: ROOT, encoding: "utf8", env: { ...process.env, GOOS: "linux", GOARCH: rel.arch } },
    );
    for (const m of new Set(out.split("\n").filter(Boolean))) add(m, "tw-guestd (in share/tailworks/vm/rootfs.ext4)");
  }
  const dirs = new Map();
  if (mods.size) {
    const out = execFileSync("go", ["mod", "download", "-json", ...mods.keys()], { cwd: ROOT, encoding: "utf8", maxBuffer: 64 << 20 });
    for (const j of out.split(/\n(?=\{)/)) {
      const o = JSON.parse(j);
      if (o.Error) errors.push(`go mod download ${o.Path}@${o.Version}: ${o.Error}`);
      else dirs.set(`${o.Path}@${o.Version}`, o.Dir);
    }
  }
  const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
  return { mods, dirs, goVersion, goroot };
}

// ---- npm packages (share/tailworks/node_modules) --------------------------
function npmPackages() {
  const base = path.join(stage, "share/tailworks");
  const pkgs = [];
  const visit = (nm) => {
    if (!fs.existsSync(nm)) return;
    for (const e of fs.readdirSync(nm, { withFileTypes: true }).sort((a, b) => (a.name < b.name ? -1 : 1))) {
      if (!e.isDirectory() || e.name.startsWith(".")) continue;
      const dirs = e.name.startsWith("@")
        ? fs.readdirSync(path.join(nm, e.name), { withFileTypes: true }).filter((s) => s.isDirectory()).map((s) => path.join(nm, e.name, s.name))
        : [path.join(nm, e.name)];
      for (const d of dirs) {
        const pj = path.join(d, "package.json");
        if (!fs.existsSync(pj)) continue;
        const p = JSON.parse(read(pj));
        const lic = typeof p.license === "string" ? p.license : p.license?.type || (Array.isArray(p.licenses) ? p.licenses.map((l) => l.type || l).join(" OR ") : "");
        pkgs.push({ name: p.name, version: p.version, license: lic || "(none declared)", dir: path.relative(stage, d), files: licenseFiles(d) });
        visit(path.join(d, "node_modules"));
      }
    }
  };
  visit(path.join(base, "node_modules"));
  return pkgs;
}

const ours = (name) => name === "create-tailworks" || name?.startsWith("@tailworks/");

// ---- microVM guest ---------------------------------------------------------
const vmDir = path.join(stage, "share/tailworks/vm");
const hasGuest = fs.existsSync(path.join(vmDir, "vmlinux"));
let manifest = null;
let kernel = null;
let vz = false;
if (hasGuest) {
  manifest = JSON.parse(read(path.join(vmDir, "manifest.json")));
  if (!Array.isArray(manifest.packages) || manifest.packages.length === 0) {
    errors.push("share/tailworks/vm/manifest.json has no \"packages\" (rebuild the guest with this tree's guest/build-image.sh)");
  }
  for (const k of ["kernel.json", "kernel-manifest.json"]) {
    if (fs.existsSync(path.join(vmDir, k))) {
      kernel = JSON.parse(read(path.join(vmDir, k)));
      vz = k === "kernel-manifest.json";
      break;
    }
  }
  if (!kernel) errors.push("share/tailworks/vm has a vmlinux but no kernel.json / kernel-manifest.json (the kernel's version and inputs)");
}
const hasFirecracker = hasGuest && fs.existsSync(path.join(vmDir, "firecracker"));

// The written offer (GPL-2.0 §3(b); LGPL-2.1 §6(c)), for the kernel and the
// GPL/LGPL packages of the rootfs.
const offer = `## Written offer

For at least three years after the tailworks project last distributes this
release, the tailworks maintainers will give any third party, on request, a
complete machine-readable copy of the corresponding source code listed above,
on a medium customarily used for software interchange, for a charge no more
than the cost of physically performing the distribution. Ask by opening an
issue at ${REPO}/issues (title: "Source request: tailworks ${rel.version}").
The sources listed above are also available at the upstream locations given,
at no charge.
`;

function kernelSource() {
  const ver = kernel.kernel_version;
  const srcSha = kernel.source_sha256 || kernel.kernel_tarball_sha256;
  let config;
  if (vz) {
    const ref = (kernel.base_config.match(/firecracker (\S+)/) || [])[1] || "v" + inputs.FIRECRACKER_VERSION;
    config = `- Base configuration: Firecracker's CI guest config,
  https://raw.githubusercontent.com/firecracker-microvm/firecracker/${ref}/resources/guest_configs/microvm-kernel-ci-aarch64-6.1.config
  (sha256 ${kernel.base_config_sha256})
- Configuration fragment, appended, then \`make olddefconfig\`:
  ${atCommit("guest/kernel/vz.config")} (sha256 ${kernel.fragment_sha256})
- Build script: ${atCommit("guest/build-kernel-vz.sh")} (target \`Image\`)`;
  } else {
    const fc = { amd64: "x86_64", arm64: "aarch64" }[rel.arch];
    config = `- Base configuration: Firecracker's CI guest config,
  ${expand(inputs.KERNEL_CONFIG_URL_BASE)}/${fc}/vmlinux-${ver}.config
  (sha256 ${kernel.base_config_sha256})
- Configuration fragment, appended, then \`make olddefconfig\`:
  ${atCommit("guest/kernel/tw.config")} (sha256 ${kernel.fragment_sha256})
- Build scripts: ${atCommit("guest/build-kernel.sh")} and the pins in
  ${atCommit("guest/inputs.sh")}${kernel.compiler ? `\n- Compiler: ${kernel.compiler}` : ""}`;
  }
  return `# Linux kernel: corresponding source

\`share/tailworks/vm/vmlinux\` is Linux ${ver}${vz ? " (arm64 Image, for the macOS Virtualization.framework backend)" : ` (${rel.arch})`},
built by tailworks ${rel.version} (commit ${rel.commit}) from unmodified
kernel.org source. No patches are applied; only the configuration below is
tailworks'. Linux is licensed under the GNU General Public License version 2
(GPL-2.0-only WITH Linux-syscall-note); the GPL-2.0 text is in this tarball's
THIRD_PARTY_NOTICES and in the source tarball (\`COPYING\`, \`LICENSES/\`).

## Corresponding source

- Kernel source: https://cdn.kernel.org/pub/linux/kernel/v${ver.split(".")[0]}.x/linux-${ver}.tar.xz
  (sha256 ${srcSha})
${config}
- The resulting \`.config\` has sha256 ${kernel.config_sha256}; the build is
  reproducible (fixed KBUILD_BUILD_* and SOURCE_DATE_EPOCH ${kernel.source_date_epoch}),
  so rebuilding from the inputs above gives the same \`vmlinux\`.

${offer}`;
}

function rootfsSource() {
  const pk = manifest.packages || [];
  const rows = pk
    .map(
      (p) => `### ${p.package} ${p.version}

- Files in rootfs.ext4: ${p.files.join(", ")}
- Copyright and license: /usr/share/doc/${p.package}/copyright in rootfs.ext4
  (read it with \`debugfs -R 'cat /usr/share/doc/${p.package}/copyright' rootfs.ext4\`)
- Source package: ${p.source} ${p.source_version},
  https://launchpad.net/ubuntu/+source/${p.source}/${p.source_version}
  (\`apt-get source ${p.source}=${p.source_version}\` on Ubuntu 24.04)`,
    )
    .join("\n\n");
  return `# rootfs.ext4: contents and corresponding source

\`share/tailworks/vm/rootfs.ext4\` is the microVM guest image built by
guest/build-image.sh for tailworks ${rel.version} (commit ${rel.commit}). It holds:

- \`/sbin/tw-guestd\`: part of tailworks (BSD-3-Clause, LICENSE), built from
  ${REPO}/tree/${rel.commit}/guest/tw-guestd; the Go modules it is
  built from are listed in THIRD_PARTY_NOTICES.
- \`/usr/bin/workerd\`: workerd ${manifest.workerd_version} (Apache-2.0), unmodified; see
  THIRD_PARTY_NOTICES.
- Unmodified files from Ubuntu ${inputs.UBUNTU_BASE_VERSION} (the pinned ubuntu-base tarball,
  ${expand(inputs.UBUNTU_BASE_URL)}), from the packages below. Some are under the
  GNU GPL or LGPL (for example glibc: LGPL-2.1-or-later; e2fsprogs' mke2fs:
  GPL-2.0); each package's copyright file, in the image, is authoritative.
  The GPL-2.0 and LGPL-2.1 texts are in THIRD_PARTY_NOTICES.

## Ubuntu packages

${rows || "(none recorded)"}

${offer}`;
}

// ---- THIRD_PARTY_NOTICES -------------------------------------------------
const go = goModules();
const npm = npmPackages();
const parts = [];
const section = (title, body) => parts.push(`${rule}\n${title}\n${rule}\n\n${body.trimEnd()}\n`);

parts.push(`THIRD-PARTY NOTICES: tailworks ${rel.version} (${rel.os}-${rel.arch})

tailworks itself is licensed under the BSD 3-Clause License (LICENSE, next to
this file). This tarball also contains third-party software, listed below with
its license. Where a license requires its text or a notice to be reproduced,
the text is included here or its path in this tarball is given.
${hasGuest ? "\nThe microVM guest (share/tailworks/vm) includes software under the GNU GPL\nand LGPL; share/tailworks/vm/KERNEL-SOURCE.md and ROOTFS-SOURCE.md say where\nits corresponding source is and include a written offer for it.\n" : ""}
Contents
  1. workerd (bin/workerd${hasGuest ? ", and /usr/bin/workerd in rootfs.ext4" : ""})
  2. Go modules compiled into the tailworks binaries
  3. JavaScript packages in share/tailworks/node_modules
${hasGuest ? `  4. The microVM guest (share/tailworks/vm)\n` : ""}  Appendix: license texts
`);

section(
  "1. workerd",
  `bin/workerd is workerd ${rel.workerd}, the npm package @cloudflare/workerd-${rel.os}-${rel.arch === "amd64" ? "64" : rel.arch}
at that version, unmodified (its integrity is checked against package-lock.json).
Copyright Cloudflare, Inc. Licensed under the Apache License, Version 2.0
(Appendix A). Source: https://github.com/cloudflare/workerd (the release
matching ${rel.workerd}).

workerd links third-party libraries (among them V8, ICU, BoringSSL, SQLite and
Cap'n Proto) under their own permissive licenses; their notices are in the
workerd source tree (build/ and the deps it fetches).`,
);

{
  const lines = [];
  lines.push(`The tailworks binaries (bin/tw*${hasGuest ? ", and tw-guestd in rootfs.ext4" : ""}) are built with Go ${go.goVersion.replace(/^go/, "")} and
statically link the Go standard library and the modules below. Each module's
license file(s) follow its entry.

${sub}
Go ${go.goVersion.replace(/^go/, "")} (the standard library and runtime)
${sub}
`);
  // Some Go distributions (Homebrew's) leave LICENSE out of GOROOT.
  const gofiles = licenseFiles(go.goroot);
  if (!gofiles.includes("LICENSE")) lines.push(`--- LICENSE ---\n${vendored("go/LICENSE").trimEnd()}\n`);
  for (const f of gofiles) lines.push(`--- ${f} ---\n${read(path.join(go.goroot, f)).trimEnd()}\n`);
  for (const [m, who] of [...go.mods].sort(([a], [b]) => (a < b ? -1 : 1))) {
    const dir = go.dirs.get(m);
    const files = dir ? licenseFiles(dir) : [];
    if (dir && !files.length) errors.push(`Go module ${m} has no license file in ${dir}`);
    lines.push(`${sub}\n${m}\nUsed by: ${[...who].sort().join(", ")}\n${sub}\n`);
    for (const f of files) lines.push(`--- ${f} ---\n${read(path.join(dir, f)).trimEnd()}\n`);
  }
  section("2. Go modules", lines.join("\n"));
}

{
  const third = npm.filter((p) => !ours(p.name));
  const lines = [
    `The vendored packages the create-tailworks templates use (deploys work offline).
Each package's own license file(s) ship in its directory; the paths are below.
@tailworks/* and create-tailworks are part of tailworks (LICENSE).
`,
  ];
  for (const p of third) {
    if (!p.files.length) warnings.push(`npm ${p.name}@${p.version} (${p.dir}) has no license file; package.json says "${p.license}"`);
    lines.push(
      `${p.name}@${p.version}: ${p.license}\n  ${p.files.length ? p.files.map((f) => `${p.dir}/${f}`).join("\n  ") : `(no license file in the package; its package.json declares "${p.license}")`}`,
    );
  }
  section("3. JavaScript packages (share/tailworks/node_modules)", lines.join("\n"));
}

if (hasGuest) {
  const b = [];
  if (hasFirecracker) {
    b.push(`share/tailworks/vm/firecracker and share/tailworks/vm/jailer are Firecracker
${inputs.FIRECRACKER_VERSION}, the unmodified release binaries from
${expand(inputs.FIRECRACKER_URL)}. Licensed under the Apache License,
Version 2.0 (Appendix A). Source: https://github.com/firecracker-microvm/firecracker
(tag v${inputs.FIRECRACKER_VERSION}). Firecracker's NOTICE and THIRD-PARTY files:

--- NOTICE ---
${vendored("firecracker/NOTICE").trimEnd()}

--- THIRD-PARTY ---
${vendored("firecracker/THIRD-PARTY").trimEnd()}
`);
  }
  if (kernel) {
    b.push(`share/tailworks/vm/vmlinux is Linux ${kernel.kernel_version}, built from unmodified kernel.org
source. GPL-2.0-only WITH Linux-syscall-note (Appendix B). Corresponding source
and a written offer: share/tailworks/vm/KERNEL-SOURCE.md.
`);
  }
  b.push(`share/tailworks/vm/rootfs.ext4 holds tw-guestd (tailworks; its Go modules are
in section 2), workerd ${manifest.workerd_version} (section 1) and files from these Ubuntu
${inputs.UBUNTU_BASE_VERSION} packages, each with its copyright file in the image
(/usr/share/doc/<package>/copyright); several are GPL or LGPL (Appendices B, C):

${(manifest.packages || []).map((p) => `  ${p.package} ${p.version} (source: ${p.source} ${p.source_version})`).join("\n")}

Corresponding source and a written offer: share/tailworks/vm/ROOTFS-SOURCE.md.
`);
  section("4. The microVM guest (share/tailworks/vm)", b.join("\n"));
}

section("Appendix A. Apache License, Version 2.0 (workerd" + (hasFirecracker ? ", Firecracker" : "") + ")", vendored("workerd/LICENSE"));
if (hasGuest) {
  section("Appendix B. GNU General Public License, version 2", vendored("GPL-2.0.txt"));
  section("Appendix C. GNU Lesser General Public License, version 2.1", vendored("LGPL-2.1.txt"));
}

for (const w of warnings) console.error(`third-party-notices: warning: ${w}`);
if (errors.length) {
  for (const e of errors) console.error(`third-party-notices: ${e}`);
  process.exit(1);
}

fs.copyFileSync(path.join(ROOT, "LICENSE"), path.join(stage, "LICENSE"));
fs.writeFileSync(path.join(stage, "THIRD_PARTY_NOTICES"), parts.join("\n"));
if (hasGuest) {
  fs.writeFileSync(path.join(vmDir, "KERNEL-SOURCE.md"), kernelSource());
  fs.writeFileSync(path.join(vmDir, "ROOTFS-SOURCE.md"), rootfsSource());
}
console.log(
  `third-party-notices: ${path.basename(stage)}: ${go.mods.size} Go modules, ${npm.filter((p) => !ours(p.name)).length} npm packages${hasGuest ? `, guest (${(manifest.packages || []).length} Ubuntu packages)` : ""}${warnings.length ? `, ${warnings.length} warnings` : ""}`,
);
