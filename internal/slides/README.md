# Slides storage model

This package defines bounded typed deck data, strict JSON decoding, canonical
encoding and content hashes. Backup restoration uses these rules to check stored
snapshots and rewrite embedded Page identity without changing slide or element
identities.

The model recognizes schema 1 and explicit schema 2 image metadata. It does not
provide image storage or transport, a write service, an editor, an API or a CLI.
The shared fixture pins canonical schema 1 bytes and their hash. Unsupported
versions, unknown fields and invalid structures fail validation.
