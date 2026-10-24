# Plan
id: plan-7f3a9c
goal: Add lease revocation
release: V1
sources:
- docs/hld.md
- docs/lld.md

## tg-1
id: 0192e4c5-7a10-7d3e-9b2f-6c1d8e4a5f30
goal: The lease refuses a write with an older fence
subsystem: Runtime Manager
touches: internal/engine/**
priority: high
points: 3
files:
- internal/lease/fence.go
- internal/engine/drive_test.go
dependencies:

### tsk-fence-stale
files: internal/lease/fence.go
done_when: go test ./internal/lease/...
estimate_lines: 120

Add the stale check to the fence: a write whose fence is below the lease's is refused and logged.

```text
### not a heading
```

- a card bullet
- WHEN a write carries an older fence THE SYSTEM SHALL refuse it
  cmd: go test ./internal/engine/...
  needs: network, docker
- IF the lease is gone THEN THE SYSTEM SHALL log it

### tsk-log
files:
- internal/engine/drive_test.go
done_when: go vet ./...
estimate_lines: 20

## tg-2
goal: Revoke a lease
points: 2
files: internal/lease/revoke.go
dependencies: tg-1

### tsk-revoke
files: internal/lease/revoke.go
estimate_lines: 40

Revoke it.

- WHEN a lease is revoked THE SYSTEM SHALL drop its fence
