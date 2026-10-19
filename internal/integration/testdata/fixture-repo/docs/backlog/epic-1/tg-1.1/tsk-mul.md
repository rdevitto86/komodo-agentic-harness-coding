files:
- calc/mul.go
- calc/mul_test.go
done_when: go test ./calc/ -run TestMul
estimate_lines: 20

Add func Mul(a, b int) int to the calc package, with a table test named TestMul.
