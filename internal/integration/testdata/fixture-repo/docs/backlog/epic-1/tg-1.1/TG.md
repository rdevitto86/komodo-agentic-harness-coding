id: 0192e4c5-7a10-7d3e-9b2f-000000000301
goal: Add Sub and Mul to the calc package
subsystem: none
priority: normal
points: 3
status: todo
files:
- calc/sub.go
- calc/sub_test.go
- calc/mul.go
- calc/mul_test.go
dependencies:

- WHEN Sub and Mul are called THE SYSTEM SHALL return the difference and the product of their arguments
  cmd: go test ./...
