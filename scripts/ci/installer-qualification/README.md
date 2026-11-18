# Complete Linux installer acceptance

Run from a clean product checkout with a complete, verified release directory:

```sh
crewship-heavy go run ./scripts/ci/installer-qualification \
  --artifact-dir /absolute/full-release \
  --installer scripts/install.sh --revision FULL_40_CHARACTER_SHA
```

The pinned Node image must already exist locally. The tool honors `DOCKER_HOST`
and never pulls images, changes host services or accesses the Vault. The release
rehearsal prepares its own pinned image on its CI runner. Public builds do not
require private evidence or credentials.

The unchanged installer is checked against Git, then downloads a synthetic
candidate archive and checksum through an isolated TLS fixture. Verification
stays enabled; only this container trusts the temporary fixture CA. No release
is published. All four installed companions must match their input hashes, the
SDK must report matching executing provenance/capabilities, and the full running
server must have the expected executing hash, clean revision, fresh database,
readiness and embedded UI. PASS requires authenticated removal of the owned
container and confirmed NotFound afterward. A gateway refusal to verify absence
is a failed qualification, never proof of cleanup. Interrupt/TERM cancels the
fixture context and attempts scoped cleanup with its separate 30-second budget.
An ambiguous create reports its fixture label and cannot claim cleanup or PASS.
Unit negative controls cover
foreign ownership, failed deletion, unknown absence and a surviving container.

This qualifies explicit-version root installation into a fresh custom prefix
and a complete server boot with `--no-docker`. It does not qualify public
GitHub distribution, release signatures, latest-version resolution, default
prefix/sudo, system service setup, Docker provisioning, model runs or credential
flows. Those are separate scenarios; archive boot alone is not installer proof.
