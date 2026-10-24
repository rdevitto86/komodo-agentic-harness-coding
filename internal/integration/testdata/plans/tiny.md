# Plan
goal: Add an Add function to the tiny package
release: V1

## tg-1
id: 0192e4c5-7a10-7d3e-9b2f-000000000101
goal: Add an Add function to the tiny package
subsystem: none
priority: normal
points: 3
files:
- tiny/add.go
dependencies:

### tsk-add
files:
- tiny/add.go
done_when: test -f tiny/add.go
estimate_lines: 10

Add func Add(a, b int) int to the tiny package.

- WHEN Add is called THE SYSTEM SHALL return the sum of its arguments
  cmd: go test ./...
