# Demonstrate Obfuscation End to End

This tutorial walks through the full obfuscation path with a real source database, a real
`ditto reseed`, and a restored copy you can inspect afterwards.

By the end you will:

- seed synthetic PII-shaped data into a Postgres or MySQL source database
- bake obfuscation into the dump during `ditto reseed`
- verify the restored copy contains transformed values instead of the raw source values
- apply the same rules post-restore with `--obfuscate` when you start from a raw dump

The exact numeric suffixes differ between Postgres and MySQL because the engines use different hash
functions. The important guarantees are the same in both engines:

- the values still look realistic
- the mapping is deterministic
- repeated raw values map to the same obfuscated value across tables

## Before you begin

You need:

- ditto installed
- a Docker-compatible runtime on the same machine
- a Postgres or MySQL source database reachable from that runtime
- a SQL client for inspecting the source and the restored copy

Do not use `localhost`, `127.0.0.1`, or `::1` as `source.host`. Dump helpers run inside
containers, so the source must be reachable from the container runtime.

## 1. Create the demo schema and seed data

Use one of the following SQL snippets against your source database.

### Postgres

```sql
CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  role TEXT NOT NULL,
  email TEXT NOT NULL,
  full_name TEXT NOT NULL,
  phone TEXT NOT NULL,
  ssn TEXT,
  notes TEXT NOT NULL,
  api_key TEXT NOT NULL,
  account_uuid UUID NOT NULL
);

CREATE TABLE payment_methods (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  brand TEXT NOT NULL,
  card_number TEXT NOT NULL,
  billing_email TEXT NOT NULL
);

CREATE TABLE audit_logs (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  action TEXT NOT NULL,
  ip_address TEXT NOT NULL,
  target_url TEXT NOT NULL,
  actor_uuid UUID NOT NULL
);

CREATE TABLE archived_customers (
  id BIGINT PRIMARY KEY,
  email TEXT NOT NULL
);

INSERT INTO users (id, role, email, full_name, phone, ssn, notes, api_key, account_uuid) VALUES
  (1, 'admin', 'alice@example.org', 'Alice Example', '+1-415-555-0101', '111-22-3333', 'Priority account', 'shared-api-key', '11111111-1111-1111-1111-111111111111'),
  (2, 'analyst', 'bob@example.org', 'Bob Example', '+1-415-555-0102', '222-33-4444', 'Needs review', 'shared-api-key', '22222222-2222-2222-2222-222222222222'),
  (3, 'viewer', 'carol@example.org', 'Carol Example', '+1-415-555-0103', '333-44-5555', 'Left voicemail', 'unique-api-key', '33333333-3333-3333-3333-333333333333');

INSERT INTO payment_methods (id, user_id, brand, card_number, billing_email) VALUES
  (10, 1, 'visa', '4111111111111111', 'alice@example.org'),
  (11, 2, 'mastercard', '5555555555554444', 'bob@example.org'),
  (12, 3, 'amex', '378282246310005', 'alice@example.org');

INSERT INTO audit_logs (id, user_id, action, ip_address, target_url, actor_uuid) VALUES
  (20, 1, 'login', '203.0.113.10', 'https://app.example.org/account', '11111111-1111-1111-1111-111111111111'),
  (21, 2, 'purchase', '198.51.100.24', 'https://pay.example.org/checkout', '22222222-2222-2222-2222-222222222222'),
  (22, 3, 'support', '192.0.2.42', 'https://support.example.org/case/42', '33333333-3333-3333-3333-333333333333');
```

### MySQL

```sql
CREATE TABLE users (
  id BIGINT PRIMARY KEY,
  role VARCHAR(32) NOT NULL,
  email VARCHAR(255) NOT NULL,
  full_name VARCHAR(255) NOT NULL,
  phone VARCHAR(32) NOT NULL,
  ssn VARCHAR(32),
  notes TEXT NOT NULL,
  api_key VARCHAR(255) NOT NULL,
  account_uuid CHAR(36) NOT NULL
);

CREATE TABLE payment_methods (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  brand VARCHAR(32) NOT NULL,
  card_number VARCHAR(32) NOT NULL,
  billing_email VARCHAR(255) NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE audit_logs (
  id BIGINT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  action VARCHAR(64) NOT NULL,
  ip_address VARCHAR(64) NOT NULL,
  target_url TEXT NOT NULL,
  actor_uuid CHAR(36) NOT NULL,
  FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE archived_customers (
  id BIGINT PRIMARY KEY,
  email VARCHAR(255) NOT NULL
);

INSERT INTO users (id, role, email, full_name, phone, ssn, notes, api_key, account_uuid) VALUES
  (1, 'admin', 'alice@example.org', 'Alice Example', '+1-415-555-0101', '111-22-3333', 'Priority account', 'shared-api-key', '11111111-1111-1111-1111-111111111111'),
  (2, 'analyst', 'bob@example.org', 'Bob Example', '+1-415-555-0102', '222-33-4444', 'Needs review', 'shared-api-key', '22222222-2222-2222-2222-222222222222'),
  (3, 'viewer', 'carol@example.org', 'Carol Example', '+1-415-555-0103', '333-44-5555', 'Left voicemail', 'unique-api-key', '33333333-3333-3333-3333-333333333333');

INSERT INTO payment_methods (id, user_id, brand, card_number, billing_email) VALUES
  (10, 1, 'visa', '4111111111111111', 'alice@example.org'),
  (11, 2, 'mastercard', '5555555555554444', 'bob@example.org'),
  (12, 3, 'amex', '378282246310005', 'alice@example.org');

INSERT INTO audit_logs (id, user_id, action, ip_address, target_url, actor_uuid) VALUES
  (20, 1, 'login', '203.0.113.10', 'https://app.example.org/account', '11111111-1111-1111-1111-111111111111'),
  (21, 2, 'purchase', '198.51.100.24', 'https://pay.example.org/checkout', '22222222-2222-2222-2222-222222222222'),
  (22, 3, 'support', '192.0.2.42', 'https://support.example.org/case/42', '33333333-3333-3333-3333-333333333333');
```

## 2. Configure ditto with the full rule set

Add the source settings you normally use, then add this obfuscation block to `ditto.yaml`:

```yaml
obfuscation:
  rules:
    - table: users
      column: email
      strategy: replace
      type: email
    - table: users
      column: full_name
      strategy: replace
      type: name
    - table: users
      column: phone
      strategy: replace
      type: phone
    - table: users
      column: ssn
      strategy: nullify
    - table: users
      column: notes
      strategy: redact
    - table: users
      column: api_key
      strategy: hash
    - table: users
      column: account_uuid
      strategy: replace
      type: uuid
    - table: payment_methods
      column: card_number
      strategy: mask
      keep_last: 4
      mask_char: "*"
    - table: payment_methods
      column: billing_email
      strategy: replace
      type: email
    - table: audit_logs
      column: ip_address
      strategy: replace
      type: ip
    - table: audit_logs
      column: target_url
      strategy: replace
      type: url
    - table: audit_logs
      column: actor_uuid
      strategy: replace
      type: uuid
```

## 3. Inspect the raw source values

Run these queries with your normal SQL client against the source database:

```sql
SELECT id, email, full_name, phone, ssn, notes, api_key, account_uuid
FROM users
ORDER BY id;

SELECT id, card_number, billing_email
FROM payment_methods
ORDER BY id;

SELECT id, ip_address, target_url, actor_uuid
FROM audit_logs
ORDER BY id;
```

Representative raw results:

```text
1 | alice@example.org | Alice Example | +1-415-555-0101 | 111-22-3333 | Priority account | shared-api-key | 11111111-1111-1111-1111-111111111111
10 | 4111111111111111 | alice@example.org
20 | 203.0.113.10 | https://app.example.org/account | 11111111-1111-1111-1111-111111111111
```

The repeated relationships matter here:

- `payment_methods.billing_email` repeats raw values from `users.email`
- `audit_logs.actor_uuid` repeats raw values from `users.account_uuid`
- `users.api_key` repeats once so you can verify deterministic hashing too

## 4. Bake obfuscation into the dump

```bash
ditto doctor
ditto reseed
```

This writes a new dump only after the obfuscation pass succeeds.

## 5. Inspect a copy restored from the obfuscated dump

Create a copy from the latest dump:

```bash
ditto copy run -- env | grep '^DATABASE_URL='
```

Connect to that copy with your SQL client and run the same inspection queries again. A
representative obfuscated result looks like this:

```text
1 | user483921@example.com | User74831 | +1-555-0147-3821 | NULL | [redacted] | 2b8c...<64 hex chars total> | 6ddf3a53-7f84-4a7d-b0f9-2bf52aa23f44
10 | ************1111 | user483921@example.com
20 | 10.42.17.3 | https://example.com/r/1a2b3c4d5e6f | 6ddf3a53-7f84-4a7d-b0f9-2bf52aa23f44
```

Verify these properties:

- emails move to `example.com`
- names become `User...`
- phones move to the fictional `+1-555-01xx-xxxx` range
- `ssn` becomes `NULL`
- `notes` becomes `[redacted]`
- `api_key` becomes a 64-character hex digest
- card numbers keep only the last 4 digits
- IPs move into `10.x.x.x`
- URLs move to `https://example.com/r/...`
- the same raw email still maps to the same obfuscated email across `users` and `payment_methods`
- the same raw UUID still maps to the same obfuscated UUID across `users` and `audit_logs`

## 6. Demonstrate post-restore obfuscation on a raw dump

If you already have a raw dump from a trusted host, you can apply the same rules after restore:

```bash
ditto copy run --dump /absolute/path/to/raw.gz --obfuscate -- env | grep '^DATABASE_URL='
```

Run the same inspection queries against that copy. The shapes should match the obfuscated dump path:
`example.com` emails, `User...` names, `10.x.x.x` IPs, masked cards, and deterministic repeated
values.

## Optional: allow an empty table without failing the run

If a column should be scrubbed when rows exist, but the table may be empty in some environments,
add a targeted `warn_only` rule:

```yaml
obfuscation:
  rules:
    - table: archived_customers
      column: email
      strategy: redact
      warn_only: true
```

Use this only for genuine zero-row cases. Missing tables or missing columns still fail the run.

## Where to go next

- Use [Run your first copy](run-your-first-copy.md) for the shortest setup path
- Use the [Configuration reference](../reference/configuration.md) for the full config surface
- Use [CI integration](../how-to/use-ditto-in-ci.md) when you want one clean copy per job
