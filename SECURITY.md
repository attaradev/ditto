# Security Policy

## Scope

This policy covers the ditto CLI, the HTTP service exposed by `ditto host`, and the supporting Go
and Python SDKs in this repository.

It does not cover:

- hardening your source database
- network policy or IAM configuration around the host running ditto
- OS-level security for the machine that owns the Docker runtime and dump files

## Reporting a vulnerability

Do not open a public GitHub issue for security vulnerabilities.

Email **<security@attara.dev>** with:

- a clear description of the issue
- steps to reproduce it
- the affected ditto version or commit
- any logs, requests, or proof-of-concept details needed to verify it

You will receive a response within 5 business days. If the issue is confirmed, we will coordinate a
fix and a disclosure timeline with you.

## Security model

### Secrets

Source database passwords and the shared-host copy-credential seed can be resolved at runtime from:

- `env:VAR`
- `file:/path/to/secret`
- `arn:aws:secretsmanager:...`

Secrets are not persisted in ditto's SQLite metadata store. They are resolved only when needed and
cached in memory for a short period.

### Network exposure

In local mode, copy containers bind to loopback on the ditto machine. In shared-host mode, copy
ports bind to `server.db_bind_host` and remote DSNs use `server.advertise_host`.

If you expose `ditto host`, require short-lived bearer tokens from your identity provider, publish
copy ports only on networks you trust, and keep TLS enabled for remote database access.

### Data handling

Copies are restored from a dump file. If that dump contains sensitive data, any copy created from it
contains the same data until obfuscation is applied.

The safest operating model is:

1. configure obfuscation rules
2. run `ditto reseed`
3. distribute or restore only the obfuscated dump

That keeps raw production data out of developer shells, CI jobs, and restored copies.

### Host trust boundary

Access to the Docker socket is effectively host-level privilege. Anyone who can control the runtime
can control ditto copy containers and, by extension, the host operating them.

Treat the machine running ditto as sensitive infrastructure:

- restrict access to the Docker socket
- encrypt local storage that contains dump files
- limit shell access to trusted operators
- rotate source credentials and the shared-host copy secret when operator access changes

## Supported versions

Security fixes are applied to the latest released version. If you are running from an unreleased
commit, upgrade to the latest release first when validating whether a vulnerability still applies.
