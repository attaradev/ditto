# Changelog

All notable changes to ditto are documented in this file.

This changelog starts with the current unreleased product snapshot. Earlier history is available in
`git log`.

## Unreleased

### Added

- Initial ditto CLI for provisioning isolated Postgres and MySQL copies from scheduled source dumps
- Copy lifecycle commands for create, run, list, delete, logs, and status inspection
- `reseed`, `daemon`, and `serve` commands for dump refresh, TTL cleanup, warm pools, and remote copy
  creation
- `env` commands for shell-friendly `DATABASE_URL` injection and cleanup
- `erd` command for Mermaid and DBML schema export from a temporary copy or the source database
- Config-driven obfuscation rules plus secret resolution from environment variables, files, and AWS
  Secrets Manager
- Go and Python SDKs for programmatic copy lifecycle management
- Repository documentation, contributor guidance, security policy, and release automation
