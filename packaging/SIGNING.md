# Release signing (deb / rpm packages, macOS notarization)

Crewship's `.deb` and `.rpm` packages are GPG-signed **when a signing key is
configured** (issue #932). Signing is conditional: with no key the release still
produces packages, just unsigned. This doc is the one-time setup.

> **Status: ACTIVE.** `GPG_SIGNING_KEY` + `GPG_SIGNING_PASSPHRASE` are set as
> repo secrets (2026-07-12). The signing key is
> `EDE8 25B5 5FF8 7F7D 442B  82BA A5BA 4669 E217 C05C` (Crewship Packages
> <packages@crewship.ai>, RSA-4096, expires 2028-07-11 — rotate before then).
> The public key is committed as [`packaging/crewship-packages.pub`](./crewship-packages.pub).

## What the maintainer sets up (once)

1. **Generate a signing keypair** (RSA 4096, 2-year expiry — rotate before it
   lapses):

   ```bash
   gpg --batch --full-generate-key <<'KEY'
   Key-Type: RSA
   Key-Length: 4096
   Name-Real: Crewship Packages
   Name-Email: packages@crewship.ai
   Expire-Date: 2y
   %no-protection
   %commit
   KEY
   ```

   To use a passphrase instead of `%no-protection`, set one and also add the
   `GPG_SIGNING_PASSPHRASE` secret below.

2. **Export the private key, base64-encoded**, for the CI secret:

   ```bash
   gpg --armor --export-secret-keys packages@crewship.ai | base64 -w0
   ```

3. **Add repo/org secrets** (Settings → Secrets and variables → Actions):

   | Secret | Value |
   |---|---|
   | `GPG_SIGNING_KEY` | the base64 blob from step 2 (**required** to enable signing) |
   | `GPG_SIGNING_PASSPHRASE` | the key passphrase, or leave unset for a `%no-protection` key |

4. **Publish the public key** so users can verify. Export and commit it as
   `packaging/crewship-packages.pub` (and put it on the docs site / a
   well-known URL):

   ```bash
   gpg --armor --export packages@crewship.ai > packaging/crewship-packages.pub
   ```

## How CI uses it

`release.yml` decodes `GPG_SIGNING_KEY` to `$RUNNER_TEMP/crewship-signing.asc`
and exports `GPG_KEY_PATH`. `.goreleaser.yml` references it conditionally:

```yaml
nfpms:
  - id: packages
    deb: { signature: { key_file: '{{ if index .Env "GPG_KEY_PATH" }}{{ .Env.GPG_KEY_PATH }}{{ end }}' } }
    rpm: { signature: { key_file: '{{ if index .Env "GPG_KEY_PATH" }}{{ .Env.GPG_KEY_PATH }}{{ end }}' } }
```

Empty `GPG_KEY_PATH` → nfpm skips signing (snapshot builds, forks). The
passphrase is read from `NFPM_PACKAGES_PASSPHRASE`.

## How users verify

```bash
# get the public key (or from the docs site)
curl -fsSLO https://raw.githubusercontent.com/crewship-ai/crewship/main/packaging/crewship-packages.pub

# deb — nfpm embeds a debsigs-style `_gpgorigin` ar member: a detached
# signature over the other members in ar order. Note: dpkg-sig CANNOT verify
# this (it only reads its own _gpgbuilder manifest format); use gpg directly
# (or debsig-verify with a policy file):
gpg --import crewship-packages.pub
mkdir deb && (cd deb && ar x ../crewship_<ver>_linux_amd64.deb)
cat deb/debian-binary deb/control.tar.* deb/data.tar.* | gpg --verify deb/_gpgorigin -

# rpm
sudo rpm --import crewship-packages.pub
rpm -K crewship_<ver>_linux_amd64.rpm      # → "digests signatures OK"
```

Both the post-release package smoke (`.github/workflows/smoke-test.yml`) and
the nightly smoke (`.github/workflows/nightly-smoke.yml`) verify the deb's
`_gpgorigin` with gpg and run `rpm -K` against a scratch rpmdb with the
committed public key imported. An **unsigned** package is a soft skip (so
secret-less forks stay green) but a **bad/invalid** signature always fails.
Nightly builds sign with the same key (`nightly.yml` materializes
`GPG_SIGNING_KEY` exactly like `release.yml`), so the signing path is
exercised on every push to main, not just at tag time.

---

# macOS code signing + notarization

Darwin binaries are Apple-signed and notarized **when the signing secrets are
configured** — same conditional, fail-open pattern as the GPG signing above:
with no secrets the release ships unsigned darwin binaries exactly as before
(snapshot/nightly runs and forks keep working).

> **Status: NOT YET ACTIVE.** Requires Apple Developer Program enrollment.
> Once the secrets below are set, the next tagged release signs + notarizes
> automatically — no workflow change needed. After the first notarized
> release, update the "macOS Gatekeeper blocks the binary" accordion in
> `docs/guides/troubleshooting.mdx` (it currently documents the unsigned
> behaviour).

## Why

A binary downloaded through a browser (or any quarantine-aware app) gets the
`com.apple.quarantine` attribute; Gatekeeper then refuses to run it unless it
is signed by a Developer ID certificate **and** notarized by Apple. Homebrew
strips quarantine and `curl | bash` never sets it, so those paths work today —
this fixes the "download the tarball from GitHub Releases" path.

Signing runs via goreleaser's built-in [quill](https://github.com/anchore/quill)
integration (`notarize:` in `.goreleaser.yml`) — pure Go, on the Linux release
runner, no Xcode. It signs + notarizes the darwin outputs of the
`crewship-darwin` and `crewship-cli` builds (2 builds × 2 arches = 4 binaries)
and waits for Apple's verdict so a rejected submission fails the release
instead of shipping binaries Gatekeeper would block.

## What the maintainer sets up (once)

1. **Enroll in the Apple Developer Program** (US$99/year, as an organization).

2. **Create a "Developer ID Application" certificate**
   (developer.apple.com → Certificates). Download it, import into a local
   Keychain, then export as `.p12` with a password. Base64-encode for CI:

   ```bash
   base64 -i developer_id_application.p12 | tr -d '\n'
   ```

3. **Create an App Store Connect API key** (App Store Connect → Users and
   Access → Integrations → App Store Connect API) with the **Developer** role.
   Note the *Issuer ID* and *Key ID*, download the `.p8` once, and
   base64-encode it:

   ```bash
   base64 -i AuthKey_XXXXXXXXXX.p8 | tr -d '\n'
   ```

4. **Add repo secrets** (Settings → Secrets and variables → Actions):

   | Secret | Value |
   |---|---|
   | `MACOS_SIGN_P12` | base64 `.p12` from step 2 (**required** — its presence enables the pipe) |
   | `MACOS_SIGN_PASSWORD` | the `.p12` export password |
   | `MACOS_NOTARY_ISSUER_ID` | App Store Connect API *Issuer ID* (UUID) |
   | `MACOS_NOTARY_KEY_ID` | App Store Connect API *Key ID* |
   | `MACOS_NOTARY_KEY` | base64 `.p8` from step 3 |

5. **Rotate before expiry.** Developer ID certificates last 5 years; API keys
   don't expire but should be rotated with personnel changes. Already-notarized
   releases keep working after expiry (notarization is stapled server-side).

## How users verify

```bash
codesign --verify --deep --strict --verbose=2 crewship
spctl --assess --type execute --verbose crewship   # → "accepted, source=Notarized Developer ID"
```

## Trade-off: reproducibility

Signing embeds a signature in the Mach-O, so signed darwin binaries are no
longer byte-identical to a from-source rebuild (`-trimpath` note in
`.goreleaser.yml`). The cosign chain is unaffected — it signs the final
(signed) artifacts in the same run.

---

## Not covered here

A full **APT/DNF repository** (signed `Release` / `repomd.xml` + hosting) is a
separate, larger effort. This only signs the individual packages. Windows
Authenticode signing for `crewship.exe` is a separate follow-up (SmartScreen
prompt documented in `docs/guides/troubleshooting.mdx`).
