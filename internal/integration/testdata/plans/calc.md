# Plan
goal: Add Sub and Mul to the calc package
release: V1

## tg-1
id: 0192e4c5-7a10-7d3e-9b2f-000000000301
goal: Add Sub and Mul to the calc package
subsystem: none
priority: normal
points: 3
files:
- calc/sub.go
- calc/sub_test.go
- calc/mul.go
- calc/mul_test.go
dependencies:

### tsk-sub
files:
- calc/sub.go
- calc/sub_test.go
done_when: test -f calc/sub.go && go test ./calc/ -run TestSub
estimate_lines: 20

Add func Sub(a, b int) int to the calc package, with a table test named TestSub.

- WHEN Sub and Mul are called THE SYSTEM SHALL return the difference and the product of their arguments
  cmd: go test ./...

### tsk-mul
files:
- calc/mul.go
- calc/mul_test.go
done_when: test -f calc/mul.go && go test ./calc/ -run TestMul
estimate_lines: 20

Add func Mul(a, b int) int to the calc package, with a table test named TestMul.
