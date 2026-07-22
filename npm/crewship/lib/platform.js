'use strict';

// Platform resolution for the `crewship` npm meta-package.
//
// The meta-package ships no binary of its own. It declares one
// optionalDependency per supported platform (`@crewship/cli-<os>-<arch>`),
// each carrying an `os` + `cpu` field so npm unpacks exactly one of them on
// any given host. At run time `bin/crewship.js` asks this module where that
// binary landed and execs it.
//
// Kept dependency-free and side-effect-free so `node --test` can exercise the
// mapping and the error text without unpacking a 75 MB tarball.

const path = require('path');

// process.platform + process.arch -> npm package name suffix.
// Only the four combinations the release pipeline actually publishes are
// listed. Windows is deliberately absent — see README.md ("Windows").
const SUPPORTED = {
  'darwin arm64': 'darwin-arm64',
  'darwin x64': 'darwin-x64',
  'linux arm64': 'linux-arm64',
  'linux x64': 'linux-x64',
};

const SCOPE = '@crewship/cli';

/**
 * @returns {string|null} the platform package name for this host, or null when
 *   the host is not one Crewship publishes binaries for.
 */
function packageNameFor(platform, arch) {
  const suffix = SUPPORTED[`${platform} ${arch}`];
  return suffix ? `${SCOPE}-${suffix}` : null;
}

/** @returns {string[]} every platform package name, sorted. */
function allPackageNames() {
  return Object.values(SUPPORTED)
    .map((suffix) => `${SCOPE}-${suffix}`)
    .sort();
}

/** @returns {{platform: string, arch: string}[]} every supported target. */
function allTargets() {
  return Object.keys(SUPPORTED).map((key) => {
    const [platform, arch] = key.split(' ');
    return { platform, arch };
  });
}

/**
 * Error thrown when the binary cannot be located. Carries `code` so callers
 * can distinguish "unsupported host" from "npm skipped the optional dep".
 */
class BinaryNotFoundError extends Error {
  constructor(message, code) {
    super(message);
    this.name = 'BinaryNotFoundError';
    this.code = code;
  }
}

function unsupportedHostMessage(platform, arch) {
  const targets = allTargets()
    .map((t) => `  - ${t.platform}/${t.arch}`)
    .join('\n');
  const windowsHint =
    platform === 'win32'
      ? '\nWindows binaries ARE published on the GitHub release page, but are not\n' +
        'distributed via npm yet. Download the .zip from\n' +
        'https://github.com/crewship-ai/crewship/releases and unpack it on your PATH.\n'
      : '';
  return (
    `crewship: no prebuilt binary for ${platform}/${arch}.\n\n` +
    `Supported platforms:\n${targets}\n${windowsHint}\n` +
    'Other install options (Homebrew, curl | bash, deb/rpm, Docker):\n' +
    '  https://docs.crewship.ai/guides/install\n'
  );
}

function missingOptionalDependencyMessage(pkgName, resolveError) {
  return (
    `crewship: the platform package "${pkgName}" is not installed.\n\n` +
    'npm should have installed it automatically as an optionalDependency.\n' +
    'The usual causes are:\n' +
    '  - the install ran with --no-optional / --omit=optional\n' +
    '  - a package-lock.json / pnpm-lock.yaml created on a different OS or CPU\n' +
    '    (npm has a long-standing bug where optional deps are pruned from a\n' +
    '     lockfile generated on another platform — npm/cli#4828)\n' +
    '  - an offline cache that never held this platform package\n\n' +
    'Fixes, in order of least surprise:\n' +
    `  npm install ${pkgName}          # add it explicitly\n` +
    '  rm -rf node_modules package-lock.json && npm install\n' +
    '  npm install --include=optional crewship\n\n' +
    (resolveError ? `Resolution error: ${resolveError.message}\n` : '')
  );
}

/**
 * Locate the crewship binary shipped by this host's platform package.
 *
 * @param {object} [opts]
 * @param {string} [opts.platform] defaults to process.platform
 * @param {string} [opts.arch] defaults to process.arch
 * @param {(request: string) => string} [opts.resolve] defaults to
 *   require.resolve; injected by the unit tests.
 * @returns {string} absolute path to the executable
 * @throws {BinaryNotFoundError}
 */
function resolveBinaryPath(opts) {
  const options = opts || {};
  const platform = options.platform || process.platform;
  const arch = options.arch || process.arch;
  const resolve = options.resolve || require.resolve;

  const pkgName = packageNameFor(platform, arch);
  if (!pkgName) {
    throw new BinaryNotFoundError(
      unsupportedHostMessage(platform, arch),
      'CREWSHIP_UNSUPPORTED_PLATFORM',
    );
  }

  // Platform packages deliberately declare no "exports" map, so a deep
  // require.resolve of the binary itself works. Resolving package.json as a
  // fallback keeps us working if that ever changes.
  const binName = 'crewship';
  try {
    return resolve(`${pkgName}/bin/${binName}`);
  } catch (deepErr) {
    try {
      const manifest = resolve(`${pkgName}/package.json`);
      return path.join(path.dirname(manifest), 'bin', binName);
    } catch (_manifestErr) {
      throw new BinaryNotFoundError(
        missingOptionalDependencyMessage(pkgName, deepErr),
        'CREWSHIP_PLATFORM_PACKAGE_MISSING',
      );
    }
  }
}

module.exports = {
  SCOPE,
  BinaryNotFoundError,
  packageNameFor,
  allPackageNames,
  allTargets,
  resolveBinaryPath,
  unsupportedHostMessage,
  missingOptionalDependencyMessage,
};
