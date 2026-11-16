# Isolated storage upgrade rehearsal

This helper belongs only to the synthetic Docker image test. It requires an
existing SQLite file, writes one dedicated marker table while its server is
stopped, and later reads that marker without creating a replacement database.
It is not an operator migration or recovery command.

The optional `upgrade_candidate_sha` input of the manual PR image workflow
builds that exact repository commit without publishing an image. The workflow
records the fixture commit separately, scans the candidate and runs the
containerized runtime harness from the candidate source directory.

Set `upgrade_from` to the previous immutable Crewship GHCR digest and
`previous_sha` to its exact embedded commit. The defaults identify the reported
incident image. Run this only on an isolated Docker runner after the requested
review/owner gate. The optional rehearsal has not been executed merely because
its source or marker unit tests pass.

The harness generates all container and volume names. It starts the unchanged
previous image behind the pinned socket proxy, verifies its embedded identity
and reproduces the rejected local bind and accepted daemon bind using creates
that are never started. It then stops the server, seeds a synthetic database
marker and archives the entire named volume offline. The original volume stays
offline and is never reused as the active restore.

Restore goes into a new private volume. File contents, symlink text, paths,
permissions and numeric ownership must match before candidate startup. The
restored instance is the sole active copy of this installation identity. The
candidate must preserve the database/HOME identity and markers, stage the
exact distributed runtime files, and pass real sidecar health, exec, data,
read-only binds, secrets, parity, restart and recreate tests. A second unrelated
installation runs concurrently on its own volume.

Before rollback the canary agents and server are stopped. The previous image
then reads the same restored database and HOME. Its original runtime path
failure is expected and explicitly reported; rollback does not repair that
version. The original offline volume must retain the same contents and ownership manifest.
No fallback empties a database, changes keys or reseals audit history. A future
schema-incompatible candidate must fail this rehearsal rather than pretend the
old binary can read its database.

The archive/restore helper is an isolated, read-only-root container with no
network or Docker socket. Its CHOWN, DAC_OVERRIDE and FOWNER capabilities apply
only to its generated synthetic volume and preserve backup metadata. Crew
container capabilities and socket-proxy API permissions are unchanged. The
archive and synthetic databases are temporary; no production data or paid model
credentials are used or uploaded. The script cleans only its own exact names
and installation labels and never accepts caller volume names.
