files:
- calc/sub.go
- calc/sub_test.go
done_when: test -f calc/sub.go && go test ./calc/ -run TestSub
estimate_lines: 20

Add func Sub(a, b int) int to the calc package, with a table test named TestSub.
