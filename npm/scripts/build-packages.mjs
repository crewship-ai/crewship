#!/usr/bin/env node
// Assemble the publishable npm package tree from ALREADY-BUILT crewship
// artifacts. This script never compiles Go and never invents a version: the
// binaries come from the GitHub release archives (checksum-verified by the
// caller) and the version comes from the release tag.
//
// Usage:
//   node npm/scripts/build-packages.mjs \
//     --version 1.0.0 \
//     --artifacts <dir> \
//     --out <dir> \
//     [--targets darwin-arm64,linux-x64] \
//     [--local-optional-deps <dir-with-tgz>]
//
// --artifacts expects one subdirectory per target, named "<os>-<arch>",
// each holding the three files the release archive ships:
//
//   <artifacts>/darwin-arm64/crewship
//   <artifacts>/darwin-arm64/crewship-sidecar   (a LINUX ELF, by design)
//   <artifacts>/darwin-arm64/entrypoint.sh
//
// --local-optional-deps rewrites the meta package's optionalDependencies to
// `file:` URLs pointing at tarballs in that directory. Used ONLY by the CI
// smoke test so it can install the whole thing without a registry; the
// published meta package always pins plain exact versions.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const NPM_DIR = path.resolve(HERE, '..');
const REPO_ROOT = path.resolve(NPM_DIR, '..');
const META_SRC = path.join(NPM_DIR, 'crewship');

// Keep in sync with npm/crewship/lib/platform.js — the unit test asserts the
// meta package.json and that module agree, and this table is what writes the
// meta package.json.
const TARGETS = [
  { key: 'darwin-arm64', os: 'darwin', cpu: 'arm64', label: 'macOS (Apple Silicon)' },
  { key: 'darwin-x64', os: 'darwin', cpu: 'x64', label: 'macOS (Intel)' },
  { key: 'linux-arm64', os: 'linux', cpu: 'arm64', label: 'Linux (arm64)' },
  { key: 'linux-x64', os: 'linux', cpu: 'x64', label: 'Linux (x86_64)' },
];

// Files a platform package ships, and the mode each needs. The daemon
// autodetects crewship-sidecar and entrypoint.sh next to its own executable
// (internal/config/config.go resolveSidecarPaths), which is why all three land
// in the same bin/ directory.
const PAYLOAD = [
  { name: 'crewship', mode: 0o755, required: true },
  { name: 'crewship-sidecar', mode: 0o755, required: true },
  { name: 'entrypoint.sh', mode: 0o755, required: true },
];

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith('--')) throw new Error(`unexpected argument: ${arg}`);
    const key = arg.slice(2);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith('--')) {
      throw new Error(`--${key} requires a value`);
    }
    out[key] = value;
    i += 1;
  }
  return out;
}

function die(msg) {
  process.stderr.write(`build-packages: ${msg}\n`);
  process.exit(1);
}

// Accepts "1.2.3", "v1.2.3", "v1.2.3-beta.1". Strips the leading v so the npm
// version tracks the release tag exactly, minus the tag prefix npm forbids.
function normalizeVersion(raw) {
  const v = raw.replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/.test(v)) {
    die(`"${raw}" is not a publishable semver version`);
  }
  return v;
}

function copyFile(src, dst, mode) {
  fs.mkdirSync(path.dirname(dst), { recursive: true });
  fs.copyFileSync(src, dst);
  fs.chmodSync(dst, mode);
}

function platformReadme(target, version) {
  return `# @crewship/cli-${target.key}

Prebuilt Crewship binary for **${target.label}**, published by the
[crewship](https://www.npmjs.com/package/crewship) release pipeline.

Do not install this package directly — install \`crewship\`, which declares
every platform package as an \`optionalDependency\` and picks the right one for
your machine:

\`\`\`bash
npm install -g crewship
\`\`\`

## Contents

| File | Notes |
|---|---|
| \`bin/crewship\` | the full daemon + CLI + embedded dashboard, \`${target.os}/${target.cpu}\` |
| \`bin/crewship-sidecar\` | **always a Linux ELF** — bind-mounted into agent containers, never run on the host |
| \`bin/entrypoint.sh\` | agent-container entrypoint, autodetected next to the binary |

These bytes come from \`crewship_${version}_${target.os}_${target.cpu === 'x64' ? 'amd64' : target.cpu}\`
on the [GitHub release](https://github.com/crewship-ai/crewship/releases/tag/v${version}),
verified against that release's \`checksums.txt\` before repacking.

Apache-2.0.
`;
}

function buildPlatformPackage({ target, version, artifactsDir, outDir }) {
  const srcDir = path.join(artifactsDir, target.key);
  if (!fs.existsSync(srcDir)) {
    die(`missing artifacts for ${target.key}: expected ${srcDir}`);
  }

  const pkgDir = path.join(outDir, `cli-${target.key}`);
  fs.rmSync(pkgDir, { recursive: true, force: true });
  fs.mkdirSync(path.join(pkgDir, 'bin'), { recursive: true });

  for (const item of PAYLOAD) {
    const src = path.join(srcDir, item.name);
    if (!fs.existsSync(src)) {
      if (item.required) die(`${target.key}: missing ${item.name} in ${srcDir}`);
      continue;
    }
    copyFile(src, path.join(pkgDir, 'bin', item.name), item.mode);
  }

  const manifest = {
    name: `@crewship/cli-${target.key}`,
    version,
    description: `Crewship prebuilt binary for ${target.label}`,
    homepage: 'https://crewship.ai',
    repository: {
      type: 'git',
      url: 'git+https://github.com/crewship-ai/crewship.git',
      directory: 'npm',
    },
    license: 'Apache-2.0',
    author: 'Crewship <support@crewship.ai>',
    // os + cpu are what make the optionalDependency mechanism work: npm
    // refuses to install a platform package on a non-matching host, so exactly
    // one of the four ever unpacks.
    os: [target.os],
    cpu: [target.cpu],
    // NO "exports" map on purpose — the meta shim deep-resolves
    // "<pkg>/bin/crewship" via require.resolve.
    files: ['bin/', 'README.md', 'LICENSE'],
    publishConfig: { access: 'public' },
  };

  fs.writeFileSync(
    path.join(pkgDir, 'package.json'),
    `${JSON.stringify(manifest, null, 2)}\n`,
  );
  fs.writeFileSync(path.join(pkgDir, 'README.md'), platformReadme(target, version));
  copyFile(path.join(REPO_ROOT, 'LICENSE'), path.join(pkgDir, 'LICENSE'), 0o644);

  return pkgDir;
}

function buildMetaPackage({ version, targets, outDir, localOptionalDeps }) {
  const pkgDir = path.join(outDir, 'crewship');
  fs.rmSync(pkgDir, { recursive: true, force: true });
  fs.mkdirSync(pkgDir, { recursive: true });

  copyFile(
    path.join(META_SRC, 'bin', 'crewship.js'),
    path.join(pkgDir, 'bin', 'crewship.js'),
    0o755,
  );
  copyFile(
    path.join(META_SRC, 'lib', 'platform.js'),
    path.join(pkgDir, 'lib', 'platform.js'),
    0o644,
  );
  copyFile(path.join(META_SRC, 'README.md'), path.join(pkgDir, 'README.md'), 0o644);
  copyFile(path.join(REPO_ROOT, 'LICENSE'), path.join(pkgDir, 'LICENSE'), 0o644);

  const manifest = JSON.parse(
    fs.readFileSync(path.join(META_SRC, 'package.json'), 'utf8'),
  );
  manifest.version = version;

  const optional = {};
  for (const target of targets) {
    const name = `@crewship/cli-${target.key}`;
    if (localOptionalDeps) {
      // The smoke test's registry-free install path.
      const tgz = path.join(
        localOptionalDeps,
        `crewship-cli-${target.key}-${version}.tgz`,
      );
      if (!fs.existsSync(tgz)) die(`--local-optional-deps: missing ${tgz}`);
      optional[name] = `file:${tgz}`;
    } else {
      optional[name] = version;
    }
  }
  manifest.optionalDependencies = optional;

  fs.writeFileSync(
    path.join(pkgDir, 'package.json'),
    `${JSON.stringify(manifest, null, 2)}\n`,
  );
  return pkgDir;
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  if (!args.version) die('--version is required');
  if (!args.out) die('--out is required');

  const version = normalizeVersion(args.version);
  const outDir = path.resolve(args.out);

  const selected = args.targets
    ? args.targets.split(',').map((k) => {
        const t = TARGETS.find((x) => x.key === k.trim());
        if (!t) die(`unknown target "${k}" (known: ${TARGETS.map((x) => x.key).join(', ')})`);
        return t;
      })
    : TARGETS;

  fs.mkdirSync(outDir, { recursive: true });

  const built = [];
  if (args.artifacts) {
    const artifactsDir = path.resolve(args.artifacts);
    for (const target of selected) {
      built.push(buildPlatformPackage({ target, version, artifactsDir, outDir }));
    }
  } else if (!args['meta-only']) {
    die('--artifacts is required unless --meta-only 1 is passed');
  }

  const metaDir = buildMetaPackage({
    version,
    targets: selected,
    outDir,
    localOptionalDeps: args['local-optional-deps']
      ? path.resolve(args['local-optional-deps'])
      : null,
  });
  built.push(metaDir);

  for (const dir of built) {
    process.stdout.write(`built ${path.relative(process.cwd(), dir)}\n`);
  }
}

main();
