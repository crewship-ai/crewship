# Backup inspection and restore work limits

`backup inspect` reads bounded metadata and stops at the payload header. Its
JSON response preserves the manifest fields and adds `integrity_verified:false`.
It does not decrypt, checksum, or prove completeness. A damaged payload can have
readable metadata. Use `backup verify` for the sealed-payload checksum; encrypted
payload completeness still requires a contents check or restore with its key.

The server and offline recovery/conversion use the same operator environment
settings. Invalid settings fail the operation. Limits cannot be raised through a
restore request or by a workspace owner. Values are decimal integer bytes/counts,
except the Go duration string for timeout.

| Variable suffix after `CREWSHIP_BACKUP_RESTORE_` | Default |
| --- | ---: |
| `COMPRESSED_BYTES` | 68719476736 (64 GiB per opened reader) |
| `DECODED_BYTES` | 137438953472 (128 GiB aggregate decoded work) |
| `ENTRIES` | 1000000 logical entries across all passes |
| `GENERATED_BYTES` | 68719476736 (64 GiB aggregate generated output) |
| `BUFFERED_BYTES` | 67108864 (64 MiB aggregate source retained/parsed in memory) |
| `HEADER_BYTES` | 268435456 (256 MiB aggregate logical names and link names) |
| `SECTION_SINKS` | 256 (aggregate extraction section files) |
| `DECODER_MEMORY_BYTES` | 67108864 (64 MiB per decoder) |
| `DECODER_WINDOW_BYTES` | 67108864 (64 MiB maximum window) |

The following operation settings use their full environment variable names:

| Operator environment variable | Default |
| --- | ---: |
| `CREWSHIP_BACKUP_RESTORE_DEPTH` | 4 simultaneously open decoders |
| `CREWSHIP_BACKUP_RESTORE_TIMEOUT` | 2h |
| `CREWSHIP_BACKUP_RESTORE_CONCURRENCY` | 1 (configurable 1–4 per process) |

By default only one top-level backup read operation runs in a process; an operation
above the configured concurrency fails immediately. Nested helpers reuse admission. Low-level reader constructors alone do not take
a process admission slot; they retain finite work/decoder limits. End-to-end
accounting requires the admitted context from the operation entrypoint. Verify, reopens, inner archives,
recovery, drills, contents checks, unsafe-settings scans and staged-environment
landing share their top-level work allowance. Conversion shares one allowance
across indexing and rewriting. Unknown entries and tar's implicit body discards
count. Both outer and inner decoded bytes count; repeated passes count again.
Generated output includes temporary repacked TAR headers/padding, image/blob transfers and direct
recovery files. Page stores reserve the matching serialized size before writing;
Git object/reference stores reserve conservative representation/metadata upper
bounds. Reservations can exceed actual writes, including already-present data. It is a monotonic work allowance: removing a file does not refund
it. Thus it is conservative, not a measurement/reservation of free disk space.
Logical entries exclude PAX/GNU extension records consumed internally by Go; their decoded bytes still count. Buffered source, names and sink limits also apply; they are conservative work allowances, not measured heap size. Existing section-specific limits still apply.

The name/link allowance counts UTF-8 bytes, not characters or the fixed 512-byte
TAR header blocks. At one million logical entries, 256 MiB permits about 268
name/link bytes per entry on average; longer paths can exhaust this independent
allowance earlier. Names stream through the reader, so this is not a reserved
256 MiB allocation. Both entry and name/link counters are shared across passes:
reading a file twice counts twice, so these defaults do not promise that every
multi-pass workflow can restore one million distinct files.

Metadata inspection also has fixed limits of 16 MiB decoded prefix, 1024 logical entries
and 30 seconds, and the existing 4 MiB manifest cap. It does not scan a large
payload looking for a late manifest. The legacy buffered `ReadBundle` helper is
limited to 64 MiB payload; streaming readers support larger inputs.

These defaults are explicit capacity assumptions, not measured compression
ratios or a promise every upload is restorable. The compressed default matches
the existing upload limit. Decoded work is not final restored size. Operators
must set limits for their capacity and may need to raise them for large archives.
Decoder memory/window bounds are not a bound on process RSS or JSON object
allocation. Synchronous decode and filesystem operations check cancellation at
read/write boundaries; this is not instruction-level CPU preemption. Admission
is process-local, not a cluster-wide lock.

On error, owned temporary extraction directories are removed after their writers
close. Existing recovery targets are never recursively deleted by this policy.
Offline instance recovery already writes directly to its destination: an error
can leave a partial target and does not establish an atomic publication boundary.
No database format, encrypted byte layout or archive format changes.
