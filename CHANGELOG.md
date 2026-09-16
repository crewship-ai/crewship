# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

### Added

- Incoming webhook configuration in Integrations for routines, agents and Page panels, with explicit outgoing notification labels. Routine webhooks can select a GitHub pull request signature profile with content-based replay protection.
- **CLI:** `routine webhooks create --ingress-profile crewship|github`, a PROFILE column on `webhooks list`, and `routine webhooks fire <url|token> --secret … --body …` — a signed test delivery to the public dispatch URL in either profile, with the receipt printed back; the docs for the agent trigger's durable-ledger response (`202 {delivery_id, work_id, status, duplicate}`, `200 ignored`, 409/413/429/503), the pipeline dispatch body and status set, `--hmac-secret`, the GitHub URL suffix and the Incoming webhooks tab are brought up to date. (#2580)
