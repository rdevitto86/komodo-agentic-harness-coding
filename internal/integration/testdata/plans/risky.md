# Plan
goal: Change the CI workflow, add Add and add Dep
release: V1

## tg-1
id: 0192e4c5-7a10-7d3e-9b2f-000000000401
goal: Add a CI workflow
subsystem: none
priority: normal
points: 1
files:
- .github/workflows/ci.yml
dependencies:

### tsk-ci
files:
- .github/workflows/ci.yml
done_when: test -f .github/workflows/ci.yml
estimate_lines: 5

Add a CI workflow file.

## tg-2
id: 0192e4c5-7a10-7d3e-9b2f-000000000402
goal: Add an Add function to the tiny package
subsystem: none
priority: normal
points: 1
files:
- tiny/add.go
dependencies:

### tsk-add
files:
- tiny/add.go
done_when: test -f tiny/add.go
estimate_lines: 10

Add func Add(a, b int) int to the tiny package.

## tg-3
id: 0192e4c5-7a10-7d3e-9b2f-000000000403
goal: Add a Dep function that waits for the workflow
subsystem: none
priority: normal
points: 1
files:
- tiny/dep.go
dependencies: tg-1

### tsk-dep
files:
- tiny/dep.go
done_when: test -f tiny/dep.go
estimate_lines: 10

Add func Dep() int to the tiny package.
