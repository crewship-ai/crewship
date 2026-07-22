# crewship

Self-hosted runtime for AI coding agents — daemon + CLI + embedded web UI in a
single Go binary. This package is a thin npm distribution wrapper around the
**exact same signed binary** published on the
[GitHub release page](https://github.com/crewship-ai/crewship/releases).

```bash
npx crewship@latest version
# or
npm install -g crewship
crewship version
```

Full documentation: <https://docs.crewship.ai>

## What you get: the FULL binary, not the CLI-only build

Crewship publishes two binary flavours. **This npm package ships the full
one.** Everything works, including `crewship start`:

| | shipped here |
|---|---|
| `crewship ask` / `crew` / `agent` / `credential` / `mission` | ✅ |
| `crewship start` — run the daemon + embedded dashboard | ✅ |
| `crewship doctor` — local container-runtime detection | ✅ |
| `crewship admin` — direct SQLite recovery | ✅ |
| `crewship telemetry`, `crewship memory log/show/restore` | ✅ |

The cost of that choice is size: each platform package is roughly **75 MB**
unpacked (a ~55 MB daemon binary with the Next.js bundle embedded, plus the
runtime companions below). If you only ever talk to a *remote* `crewshipd`,
the ~27 MB `crewship-cli` Homebrew formula or the release tarball is a leaner
choice — but it cannot start a server, and shipping that here would mean
`npx crewship start` failing for everyone who tried the obvious thing.

### Runtime companions are bundled

`crewship start` bind-mounts two host files into every agent container:
`crewship-sidecar` and `entrypoint.sh`. The daemon autodetects them **next to
its own executable**, so this package installs all three into the same
directory:

```
node_modules/@crewship/cli-<os>-<arch>/bin/
  crewship            # the daemon+CLI for your OS/arch
  crewship-sidecar    # ALWAYS a Linux ELF — see below
  entrypoint.sh
```

`crewship-sidecar` is a **Linux** binary even inside the macOS packages. It is
bind-mounted into Linux agent containers (Docker Desktop / Colima / OrbStack)
and executed there, never on your host. `file` reporting it as an ELF
executable on a Mac is expected.

Running a container runtime is only required if you want to run agents. A
dashboard-only host works with `crewship start --no-docker`.

## Supported platforms

| OS | arch | package |
|---|---|---|
| macOS | arm64 (Apple Silicon) | `@crewship/cli-darwin-arm64` |
| macOS | x64 (Intel) | `@crewship/cli-darwin-x64` |
| Linux | arm64 | `@crewship/cli-linux-arm64` |
| Linux | x64 | `@crewship/cli-linux-x64` |

The binaries are statically linked with `CGO_ENABLED=0`, so glibc and musl
(Alpine) hosts both work with no libc-specific variant.

**Windows is not published to npm.** Native Windows builds *do* ship on every
release, but as `.zip` archives whose packaging and signal handling are still
marked beta — the same reason the project's `install.sh` and post-release
smoke matrix exclude it. Download
`crewship_<version>_windows_<arch>.zip` from the
[releases page](https://github.com/crewship-ai/crewship/releases) instead. If
you run `npx crewship` on Windows you get a message saying exactly that
rather than npm's opaque `EBADPLATFORM`.

## How it installs — no postinstall script

This package declares one `optionalDependency` per platform, each with
matching `os` and `cpu` fields, so npm downloads and unpacks **only** the one
matching your machine. That is the same mechanism `esbuild`, `turbo` and
`biome` use.

There is deliberately **no postinstall download script**. Installs therefore
work with `npm ci --ignore-scripts`, behind a corporate proxy, and from a
warm offline npm cache.

### "the platform package is not installed"

If the `crewship` command prints that, npm skipped the optional dependency.
The usual causes are `--no-optional` / `--omit=optional`, a lockfile
generated on a different OS or CPU (npm/cli#4828), or an offline cache that
never held your platform's package. The error message lists the fixes; the
shortest is:

```bash
npm install @crewship/cli-$(node -p "process.platform + '-' + process.arch")
```

## Verifying what you installed

The binaries in these packages are byte-identical to the ones in the
corresponding GitHub release archive, which is covered by `checksums.txt` and
signed keyless with [Sigstore cosign](https://www.sigstore.dev/). The npm
publish job downloads the release artifacts and verifies them against
`checksums.txt` before repacking — it never rebuilds from source.

To check for yourself:

```bash
shasum -a 256 "$(node -p "require('crewship/lib/platform').resolveBinaryPath()")"
# compare against crewship_<version>_<os>_<arch>.tar.gz's contents
```

## Other install methods

Homebrew, `curl | bash`, `.deb` / `.rpm`, and Docker are all first-class:
<https://docs.crewship.ai/guides/install>

## License

Apache-2.0
