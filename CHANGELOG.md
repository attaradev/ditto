# Changelog

All notable changes to ditto are documented in this file.

This changelog starts with the current unreleased product snapshot. Earlier history is available in
`git log`.

## 0.3.0 — 2026-04-30

### Added

- `ditto target refresh <name>` — restore a configured dump into a named staging or QA database
  (Postgres and MySQL); requires `allow_destructive_refresh: true` and `--confirm <name>`
- `DITTO_SERVER` environment variable — shared-host clients no longer need to pass `--server` on
  every command; an explicit flag still takes precedence
- `ditto doctor --server <url>` — diagnose a remote shared host without local Docker or source config
- `ditto erd` — auto-infer engine and database from a copy DSN when source config is absent
- Server: admin-only `POST /v2/targets/{name}/refresh` API endpoint
- Config: `targets` map with per-target engine, port, credential, and `allow_destructive_refresh`
  validation

### Fixed

- Sanitize dump URI and local path in `dumpfetch` to prevent path traversal
- Resolve CodeQL and Dependabot security alerts
- GoReleaser deprecations and markdownlint failures in CI

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
