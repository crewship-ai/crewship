### Added

- Show canonical human channel requests, delegated issues and authorized host-chat turns in the agent overview, with UI and CLI send/task actions and source-bound explicit agent read receipts.

### Fixed

- Keep original requester and source-version authority through channel dispatch, reply and retries; make public issue creation retries atomic and idempotent.
- Show ordinary failed agent runs with links and an archive action instead of a routine retry action.
