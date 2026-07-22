'use strict';

// Unit tests for the npm shim's platform mapping and error paths.
// Plain `node --test` — no test framework dependency, nothing added to the
// repo-root package.json (which is the Next.js dashboard).
//
//   node --test "npm/crewship/test/*.tests.js"
//
// NOTE the `.tests.js` suffix, not `.test.js`: the repo root runs `vitest run`
// with the default include glob (`**/*.{test,spec}.?(c|m)[jt]s?(x)`) and no
// path filter, so a file named `platform.test.js` here would be collected by
// vitest — which cannot run `node:test` assertions — and redden the Frontend
// Test job. Verified with `npx vitest list`.

const test = require('node:test');
const assert = require('node:assert');
const path = require('node:path');
const fs = require('node:fs');

const platform = require('../lib/platform');
const {
  packageNameFor,
  allPackageNames,
  allTargets,
  resolveBinaryPath,
  BinaryNotFoundError,
} = platform;

test('maps every published OS/arch pair to its scoped package', () => {
  assert.equal(packageNameFor('darwin', 'arm64'), '@crewship/cli-darwin-arm64');
  assert.equal(packageNameFor('darwin', 'x64'), '@crewship/cli-darwin-x64');
  assert.equal(packageNameFor('linux', 'arm64'), '@crewship/cli-linux-arm64');
  assert.equal(packageNameFor('linux', 'x64'), '@crewship/cli-linux-x64');
});

test('returns null for platforms we do not publish', () => {
  assert.equal(packageNameFor('win32', 'x64'), null);
  assert.equal(packageNameFor('linux', 'ppc64'), null);
  assert.equal(packageNameFor('freebsd', 'x64'), null);
  assert.equal(packageNameFor('darwin', 'ia32'), null);
});

test('the mapping matches the meta-package optionalDependencies exactly', () => {
  const pkg = JSON.parse(
    fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'),
  );
  assert.deepEqual(
    Object.keys(pkg.optionalDependencies).sort(),
    allPackageNames(),
    'lib/platform.js and package.json disagree about supported platforms',
  );
  // Every optional dep must be pinned to the meta version, exactly — a range
  // would let npm hand a v1.2.0 shim a v1.3.0 binary.
  for (const [name, range] of Object.entries(pkg.optionalDependencies)) {
    assert.equal(range, pkg.version, `${name} must be pinned to ${pkg.version}`);
  }
});

test('this host is one of the advertised targets (sanity for the CI smoke)', () => {
  const targets = allTargets().map((t) => `${t.platform}/${t.arch}`);
  assert.ok(targets.length === 4, `expected 4 targets, got ${targets.length}`);
});

test('resolveBinaryPath returns the deep-resolved binary path', () => {
  const fake = '/tmp/nm/@crewship/cli-linux-x64/bin/crewship';
  const resolved = resolveBinaryPath({
    platform: 'linux',
    arch: 'x64',
    resolve: (request) => {
      assert.equal(request, '@crewship/cli-linux-x64/bin/crewship');
      return fake;
    },
  });
  assert.equal(resolved, fake);
});

test('falls back to package.json resolution when the deep path is not exported', () => {
  const manifest = path.join('/tmp/nm/@crewship/cli-darwin-arm64', 'package.json');
  const resolved = resolveBinaryPath({
    platform: 'darwin',
    arch: 'arm64',
    resolve: (request) => {
      if (request.endsWith('/bin/crewship')) {
        throw new Error('ERR_PACKAGE_PATH_NOT_EXPORTED');
      }
      return manifest;
    },
  });
  assert.equal(
    resolved,
    path.join('/tmp/nm/@crewship/cli-darwin-arm64', 'bin', 'crewship'),
  );
});

test('unsupported host: actionable error, not a resolution failure', () => {
  assert.throws(
    () => resolveBinaryPath({ platform: 'win32', arch: 'x64', resolve: () => {
      throw new Error('should never be called');
    } }),
    (err) => {
      assert.ok(err instanceof BinaryNotFoundError);
      assert.equal(err.code, 'CREWSHIP_UNSUPPORTED_PLATFORM');
      assert.match(err.message, /no prebuilt binary for win32\/x64/);
      // Windows users must be pointed at the .zip, not left guessing.
      assert.match(err.message, /releases/);
      assert.match(err.message, /guides\/install/);
      return true;
    },
  );
});

test('missing optional dependency: names the package and the npm bug', () => {
  assert.throws(
    () => resolveBinaryPath({ platform: 'linux', arch: 'arm64', resolve: () => {
      const err = new Error("Cannot find module '@crewship/cli-linux-arm64'");
      err.code = 'MODULE_NOT_FOUND';
      throw err;
    } }),
    (err) => {
      assert.ok(err instanceof BinaryNotFoundError);
      assert.equal(err.code, 'CREWSHIP_PLATFORM_PACKAGE_MISSING');
      assert.match(err.message, /@crewship\/cli-linux-arm64/);
      assert.match(err.message, /--no-optional/);
      assert.match(err.message, /npm\/cli#4828/);
      assert.match(err.message, /npm install @crewship\/cli-linux-arm64/);
      return true;
    },
  );
});

test('the bin shim is executable and has a node shebang', () => {
  const shim = path.join(__dirname, '..', 'bin', 'crewship.js');
  const first = fs.readFileSync(shim, 'utf8').split('\n')[0];
  assert.equal(first, '#!/usr/bin/env node');
  // npm sets the bit on install, but keeping it in git avoids surprises for
  // anyone running the shim straight out of a checkout.
  assert.ok(fs.statSync(shim).mode & 0o111, 'bin/crewship.js must be executable');
});
