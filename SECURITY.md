# Security Policy

## Scope

This policy covers the ditto CLI and its supporting packages. It does not cover the configuration
of your RDS source database, AWS IAM policies, or EC2 host hardening — those are your
responsibility.

## Reporting a vulnerability

Please do not open a public GitHub issue for security vulnerabilities.

Email **<security@attara.dev>** with:

- A description of the vulnerability
- Steps to reproduce
- The version of ditto affected

You will receive a response within 5 business days. If the issue is confirmed we will work on a
fix and coordinate a disclosure timeline with you.

## Security model

**Credentials** — RDS passwords are stored in AWS Secrets Manager and referenced by ARN in
`ditto.yaml`. Passwords are never written to SQLite or logged. The in-memory cache has a
5-minute TTL.

**Network isolation** — copy containers bind exclusively to `127.0.0.1`. They are not reachable
from outside the EC2 host. No ports are opened to the internet.

**Data access** — the dump user has `SELECT` privileges only. Copies are isolated from the
source — no connection from a copy back to RDS is ever made.

**Copy data** — copies contain real production data and are local to the EC2 host. EBS volume
encryption should be enabled. Copies are destroyed after TTL expiry. `DATABASE_URL` is masked in
Actions logs via `::add-mask::`.

**Docker socket** — ditto requires access to the Docker socket (`/var/run/docker.sock`). This is
equivalent to root on the host. Ensure the runner user's group membership is appropriately
restricted.

## Supported versions

Only the latest release receives security fixes.
