id: 019a0000-7c3e-7a41-9b2d-5e8f1c4a6d01
goal: Wrap the two Go lines over the 125-column limit in wire.go and decide_test.go without changing behaviour
subsystem: none
touches:
- cmd/komodo/wire.go
- internal/engine/decide_test.go
priority: normal
points: 1
status: todo
files:
- cmd/komodo/wire.go
- internal/engine/decide_test.go
dependencies:

- WHEN the repository is built after the wrap THE SYSTEM SHALL compile with exit code 0
  cmd: go build ./...
- WHEN the engine package tests run after the wrap THE SYSTEM SHALL pass with exit code 0
  cmd: go test ./internal/engine/...
- WHEN the komodo command package tests run after the wrap THE SYSTEM SHALL pass with exit code 0
  cmd: go test ./cmd/komodo/...
