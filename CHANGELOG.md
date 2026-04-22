# Changelog

All notable changes to ditto are documented in this file.

This changelog starts with the current unreleased product snapshot. Earlier history is available in
`git log`.

## 0.2.0 — 2026-04-22

### Breaking Changes

- **shared-host v2 control plane**: the host daemon now uses a redesigned v2 control plane;
  existing shared-host deployments must be redeployed before connecting CLI clients at this
  version

### Added

- Setup diagnostics and shared-host safeguards for `ops` commands

### Fixed

- JavaScript SDK: strip `git+` prefix from repository URL for npm provenance
- JavaScript SDK: package name reverted to `@attaradev/ditto-sdk`

## 0.1.0 — 2026-04-04

### Added

- Initial ditto CLI for provisioning isolated Postgres and MySQL copies from scheduled source dumps
- Copy lifecycle commands for create, run, list, delete, logs, and status inspection
- `reseed`, `daemon`, and `serve` commands for dump refresh, TTL cleanup, warm pools, and remote copy
  creation
- `env` commands for shell-friendly `DATABASE_URL` injection and cleanup
- `erd` command for Mermaid and DBML schema export from a temporary copy or the source database
- Config-driven obfuscation rules plus secret resolution from environment variables, files, and AWS
  Secrets Manager
- Go, Python, and TypeScript/JavaScript SDKs for programmatic copy lifecycle management
- Repository documentation, contributor guidance, security policy, and release automation
