# What

<!-- What changes for users, operators, or contributors? -->

## Why

<!-- What problem, risk, or reliability issue does this address? -->

## Risks

<!-- Rollout, compatibility, or operational risk. Write "none" if not applicable. -->

## Validation

- [ ] Unit tests pass (`go test -race ./...`)
- [ ] Integration tests pass if applicable (`go test -tags integration ./...`)
- [ ] Manual validation notes included when relevant

## Checklist

- [ ] No new dependencies without justification
- [ ] Engine interface unchanged, or an issue was opened first
- [ ] SQLite schema changes include a migration
