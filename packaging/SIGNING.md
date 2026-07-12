# Package signing (deb / rpm)

Crewship's `.deb` and `.rpm` packages are GPG-signed **when a signing key is
configured** (issue #932). Signing is conditional: with no key the release still
produces packages, just unsigned. This doc is the one-time setup.

> **Status: ACTIVE.** `GPG_SIGNING_KEY` + `GPG_SIGNING_PASSPHRASE` are set as
> repo secrets (2026-07-12). The signing key is
> `EDE825B55FF87F7D442B82BAA5BA4669E217C05C` (Crewship Packages
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

# deb
sudo apt-get install -y dpkg-sig
dpkg-sig --verify crewship_<ver>_linux_amd64.deb

# rpm
sudo rpm --import crewship-packages.pub
rpm -K crewship_<ver>_linux_amd64.rpm      # → "digests signatures OK"
```

The post-release package smoke (`.github/workflows/smoke-test.yml`) runs `rpm -K`
/ `dpkg-sig --verify` and treats an **unsigned** package as a soft skip (so it
stays green until the key is provisioned) but **fails on a bad/invalid**
signature once signing is on.

## Not covered here

A full **APT/DNF repository** (signed `Release` / `repomd.xml` + hosting) is a
separate, larger effort. This only signs the individual packages. macOS
notarization is tracked in the #932 discussion.
