### Added

- Native agent `keeper request`, `keeper execute --command-stdin` and `keeper request-status` commands use the current run capability through the local sidecar. They preserve Keeper policy, return bounded decision metadata, and distinguish authorization, pending approval and command exit results.
