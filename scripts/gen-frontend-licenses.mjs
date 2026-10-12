#!/usr/bin/env node
// gen-frontend-licenses.mjs — collect the actual license/NOTICE texts of
// every npm dependency of the embedded Next.js build, from the lockfile-
// installed tree (node_modules/.pnpm), plus an attribution inventory and a
// SHA-256 manifest used by the artifact-content checks.
//
// Runs with plain node inside the Docker frontend stage or on the host
// (`make licenses`); no network, no registry access — the pnpm store layout
// under node_modules/.pnpm/<entry>/node_modules/[<scope>/]<name> is the
// locked dependency set. Scoped packages (@radix-ui/react-dialog, @sentry/
// nextjs, @tanstack/react-query, …) live one level deeper: the entry's
// node_modules contains the SCOPE directory, the package below it.
//
// Output (under the directory given as $1, default build/licenses/frontend):
//   npm-licenses.json     inventory: name, version, license, text files
//   manifest.tsv          name \t version \t file \t sha256 (per text file)
//   texts/<name>@<ver>/…  the license/NOTICE files themselves
//
// A package without a license file FAILS unless it is on the reviewed,
// version-scoped exception allowlist below (evidence-cited; the notice we
// write records the declaration — it is not a copy of the upstream license).
import { readdir, readFile, writeFile, mkdir, cp, stat } from "node:fs/promises";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";

const outRoot = process.argv[2] || "build/licenses/frontend";
const pnpmDir = "node_modules/.pnpm";

// Reviewed exceptions — name@version → evidence for the license declaration.
// Adding an entry requires citing where the declaration is observable and a
// review note; versions not listed here fail hard.
const EXCEPTIONS = {
  // Dependency refresh (2026-10-05): inspected the exact registry tarballs
  // linked in PR #2915 and checked their package.json license declarations.
  // None ships a package-level license text; provider-utils only includes a
  // nested third-party zod3-to-json-schema license, not its Apache-2.0 text.
  "@ai-sdk/provider-utils@5.0.53": "package.json declares Apache-2.0",
  "@next/env@16.3.8": "package.json declares MIT",
  "@next/swc-linux-x64-gnu@16.3.8": "package.json declares MIT",
  "@next/swc-linux-x64-musl@16.3.8": "package.json declares MIT",
  // Dependency refresh (2026-09-30): inspected each exact installed package
  // in node_modules/.pnpm. These versions still omit license files and
  // declare the same license as their previously reviewed versions below.
  "@ai-sdk/provider-utils@5.0.52": "package.json declares Apache-2.0",
  "@esbuild/linux-x64@0.28.2": "package.json declares MIT",
  "@img/sharp-libvips-linux-x64@1.3.4": "package.json declares LGPL-3.0-or-later",
  "@img/sharp-libvips-linuxmusl-x64@1.3.4": "package.json declares LGPL-3.0-or-later",
  "@napi-rs/canvas-linux-x64-gnu@1.0.9": "package.json declares MIT",
  "@napi-rs/canvas-linux-x64-musl@1.0.9": "package.json declares MIT",
  "@next/env@16.3.7": "package.json declares MIT",
  "@next/swc-linux-x64-gnu@16.3.7": "package.json declares MIT",
  "@next/swc-linux-x64-musl@16.3.7": "package.json declares MIT",
  "@rolldown/binding-linux-x64-gnu@1.2.11": "package.json declares MIT",
  "@rolldown/binding-linux-x64-musl@1.2.11": "package.json declares MIT",
  "@rollup/rollup-linux-x64-gnu@4.63.5": "package.json declares MIT",
  "@rollup/rollup-linux-x64-musl@4.63.5": "package.json declares MIT",
  "@sentry/server-utils@10.75.3": "package.json declares MIT",
  // Reviewed, version-scoped allowlist (2026-09-28 S1/S4 fix): every entry
  // ships NO license file while its installed package.json declares the
  // license noted in the evidence string. Declarations were read from the
  // lockfile-installed tree (node_modules/.pnpm). Anything not listed here
  // fails hard. The generated EXCEPTION-NOTICE records the declaration; it
  // is NOT a copy of the upstream license text.
  "@ai-sdk/provider-utils@5.0.45": "package.json declares Apache-2.0",
  "@esbuild/linux-x64@0.28.1": "package.json declares MIT",
  "@img/sharp-libvips-linux-x64@1.3.3": "package.json declares LGPL-3.0-or-later",
  "@img/sharp-libvips-linuxmusl-x64@1.3.3": "package.json declares LGPL-3.0-or-later",
  "@napi-rs/canvas-linux-x64-gnu@1.0.8": "package.json declares MIT",
  "@napi-rs/canvas-linux-x64-musl@1.0.8": "package.json declares MIT",
  "@napi-rs/lzma-linux-x64-gnu@1.5.1": "package.json declares MIT",
  "@next/env@16.3.5": "package.json declares MIT",
  "@next/swc-linux-x64-gnu@16.3.5": "package.json declares MIT",
  "@next/swc-linux-x64-musl@16.3.5": "package.json declares MIT",
  "@rollup/rollup-linux-x64-gnu@4.63.2": "package.json declares MIT",
  "@rollup/rollup-linux-x64-musl@4.63.2": "package.json declares MIT",
  "@schummar/icu-type-parser@1.21.5": "package.json declares MIT",
  "@sentry/cli-linux-x64@2.58.6": "package.json declares FSL-1.1-MIT",
  "@sentry/server-utils@10.75.0": "package.json declares MIT",
  "@swc/counter@0.1.3": "package.json declares Apache-2.0",
  "agent-base@6.0.2": "package.json declares MIT",
  "client-only@0.0.1": "package.json declares MIT",
  "embla-carousel-react@8.6.0": "package.json declares MIT",
  "embla-carousel-reactive-utils@8.6.0": "package.json declares MIT",
  "embla-carousel@8.6.0": "package.json declares MIT",
  "esrecurse@4.3.0": "package.json declares BSD-2-Clause",
  "glob-to-regexp@0.4.1": "package.json declares BSD-2-Clause",
  "https-proxy-agent@5.0.1": "package.json declares MIT",
  "is-reference@1.2.1": "package.json declares MIT",
  "postgres@3.4.7": "package.json declares Unlicense",
  "react-arborist@3.16.0": "package.json declares MIT",
  "react-remove-scroll-bar@2.3.8": "package.json declares MIT",
  "rehype-katex@7.0.1": "package.json declares MIT",
  "remark-math@6.0.0": "package.json declares MIT",
  "remeda@2.33.4": "package.json declares MIT",
  "stackback@0.0.2": "package.json declares MIT",
  "tr46@0.0.3": "package.json declares MIT",
  "use-composed-ref@1.4.0": "package.json declares MIT",
  "victory-vendor@37.3.6": "package.json declares MIT AND ISC",
  "@humanfs/types@0.15.0": "package.json declares Apache-2.0",
  "@prisma/dev@0.24.17": "package.json declares ISC",
  "@radix-ui/react-compose-refs@1.1.2": "package.json declares MIT",
  "@radix-ui/react-use-layout-effect@1.1.1": "package.json declares MIT",
  "@rolldown/binding-linux-x64-gnu@1.2.8": "package.json declares MIT",
  "@rolldown/binding-linux-x64-musl@1.2.8": "package.json declares MIT",
  "imurmurhash@0.1.4": "package.json declares MIT (dev-tree fallback)",
  "natural-compare@1.4.0": "package.json declares MIT (dev-tree fallback)",
};

const licenseStems = new Set(["license", "licence", "copying", "notice", "patents"]);
const licenseExts = ["", ".md", ".txt", ".mit", ".apache", "-mit", "-apache"];

function isLicenseFile(name) {
  const lower = name.toLowerCase();
  for (const stem of licenseStems) {
    for (const ext of licenseExts) if (lower === stem + ext) return true;
    if (lower.startsWith(stem + "-") || lower.startsWith(stem + ".")) return true;
  }
  return false;
}

const sha256 = (p) => createHash("sha256").update(readFileSync(p)).digest("hex");

if (!existsSync(pnpmDir)) {
  console.error(`gen-frontend-licenses: ${pnpmDir} missing — run pnpm install first`);
  process.exit(1);
}

// The DISTRIBUTED frontend is the static export: code from production
// dependencies. Native build tooling (esbuild/rollup/swc platform binaries,
// prisma dev) is not distributed, so its texts are not bundled. The expected
// set is determined INDEPENDENTLY of the store walk, by pnpm itself from the
// lockfile (`--prod`), and the walk is checked against it in both
// directions: a walked package outside the set is skipped, an expected
// package missing from the walk fails.
const expected = new Set();
try {
  for (const group of Object.values(
    JSON.parse(execFileSync("pnpm", ["licenses", "list", "--prod", "--json"], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] })),
  )) {
    for (const pkg of group) {
      for (const v of pkg.versions || []) expected.add(`${pkg.name}@${v}`);
    }
  }
} catch (err) {
  // `pnpm licenses` needs the store index, which cache-mounted stores in
  // container builds may not carry (ERR_PNPM_MISSING_PACKAGE_INDEX_FILE).
  // Bundling the FULL installed tree then over-includes build tooling —
  // attribution-safe — and never under-includes a production dependency.
  // The artifact checks still verify content == manifest either way.
  console.error(
    `gen-frontend-licenses: prod set unavailable (${err.code || err.message?.slice(0, 60)}); ` +
    "collecting ALL installed packages including dev tooling");
}

const entries = await readdir(pnpmDir, { withFileTypes: true });
const seen = new Map(); // name@version -> inventory entry (dedupes peer variants)
const missing = [];
const meta = [];
let texts = 0;
const manifestRows = [["name", "version", "file", "sha256"]];

for (const entry of entries) {
  if (!entry.isDirectory() || entry.name === ".modules.yaml") continue;
  const nmDir = path.join(pnpmDir, entry.name, "node_modules");
  if (!existsSync(nmDir)) continue;
  // List package directories, descending into @scope/ one level: scoped
  // packages are <scope-dir>/<pkg-dir>, unscoped are <pkg-dir>.
  const packageDirs = [];
  for (const d of await readdir(nmDir, { withFileTypes: true })) {
    if (!d.isDirectory() || d.name.startsWith(".")) continue;
    if (d.name.startsWith("@")) {
      const scopeDir = path.join(nmDir, d.name);
      for (const sd of await readdir(scopeDir, { withFileTypes: true })) {
        if (sd.isDirectory() && !sd.name.startsWith(".")) {
          packageDirs.push([`${d.name}/${sd.name}`, path.join(scopeDir, sd.name)]);
        }
      }
    } else {
      packageDirs.push([d.name, path.join(nmDir, d.name)]);
    }
  }
  for (const [pkgPath, pkgDir] of packageDirs) {
    let m;
    try {
      m = JSON.parse(await readFile(path.join(pkgDir, "package.json"), "utf8"));
    } catch (err) {
      // A directory under .pnpm/<entry>/node_modules without a parsable
      // package.json is a broken install, not a layout artefact: the tree
      // comes from the lockfile and must be complete.
      meta.push(`${pkgPath}: unreadable package.json (${err.code || err.message})`);
      continue;
    }
    if (!m.name || !m.version) {
      meta.push(`${pkgPath}: package.json missing name/version`);
      continue;
    }
    const key = `${m.name}@${m.version}`;
    if (expected.size > 0 && !expected.has(key)) continue; // build tooling / non-production dep
    if (seen.has(key)) continue; // peer-variant store entry of the same package
    const found = [];
    for (const f of await readdir(pkgDir)) {
      if (!isLicenseFile(f)) continue;
      const st = await stat(path.join(pkgDir, f));
      if (st.isFile() && st.size > 0) found.push(f);
    }
    if (found.length === 0) {
      const why = EXCEPTIONS[key];
      if (why) {
        const dest = path.join(outRoot, "texts", key);
        await mkdir(dest, { recursive: true });
        const note =
          `# License notice for ${key}\n\n` +
          `This package ships no license file. Evidence for its license\n` +
          `declaration: ${why}.\n\n` +
          `This notice records the declaration; it is NOT a copy of the\n` +
          `upstream license text and not a complete license settlement.\n` +
          `The canonical text is published by OSI/SPDX.\n`;
        await writeFile(path.join(dest, "EXCEPTION-NOTICE.md"), note);
        manifestRows.push([m.name, m.version, "EXCEPTION-NOTICE.md",
          createHash("sha256").update(note).digest("hex")]);
        seen.set(key, { name: m.name, version: m.version, license: m.license || null,
          text_files: ["EXCEPTION-NOTICE.md"], exception: true });
        texts++;
        continue;
      }
      missing.push(`${key} (declares: ${JSON.stringify(m.license ?? null)})`);
      continue;
    }
    const dest = path.join(outRoot, "texts", key);
    await mkdir(dest, { recursive: true });
    for (const f of found) {
      await cp(path.join(pkgDir, f), path.join(dest, f));
      manifestRows.push([m.name, m.version, f, sha256(path.join(pkgDir, f))]);
      texts++;
    }
    seen.set(key, { name: m.name, version: m.version, license: m.license || null, text_files: found });
  }
}

for (const key of expected) {
  if (!seen.has(key) && !missing.some((m) => m.startsWith(key + " "))) {
    console.error(`gen-frontend-licenses: expected production dependency ${key} not found in the store walk`);
    process.exit(1);
  }
}

if (meta.length > 0) {
  console.error(`gen-frontend-licenses: unreadable package metadata (${meta.length}):`);
  for (const x of meta) console.error(`  ${x}`);
  process.exit(1);
}

const inventory = [...seen.values()].sort((a, b) =>
  `${a.name}@${a.version}`.localeCompare(`${b.name}@${b.version}`));
await mkdir(outRoot, { recursive: true });
await writeFile(path.join(outRoot, "npm-licenses.json"), JSON.stringify(inventory, null, 1) + "\n");
await writeFile(path.join(outRoot, "manifest.tsv"),
  manifestRows.map((r) => r.join("\t")).join("\n") + "\n");

console.log(`gen-frontend-licenses: ${inventory.length} packages, ${texts} text files -> ${outRoot}`);
if (missing.length > 0) {
  console.error(`gen-frontend-licenses: MISSING license texts for ${missing.length} packages:`);
  for (const m of missing) console.error(`  ${m}`);
  process.exit(1);
}
