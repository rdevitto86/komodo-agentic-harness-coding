files: internal/engine/decide_test.go
done_when: go test ./internal/engine/...
estimate_lines: 6

In `internal/engine/decide_test.go`, function `TestBuilderNeedsScopeEndBlocksAndTheFollowingAskIsNotSpentTwice`, line 570 is an `if` condition over 125 columns (tab width 4):

```go
if a.State.Stage != StageBlocked || a.State.Block.Reason != state.ReasonNeedsScope || slices.Contains(kinds(a), OrderScope) {
```

Hoist part of the condition into a named local variable declared on the line just above the `if`, for example:

```go
scoped := slices.Contains(kinds(a), OrderScope)
if a.State.Stage != StageBlocked || a.State.Block.Reason != state.ReasonNeedsScope || scoped {
```

Pick a name not already used in that function. The truth value must stay identical; `kinds(a)` has no side effects, so evaluating it before the `if` changes nothing. Leave the `t.Fatalf` line and every other line untouched. Run `gofmt -w internal/engine/decide_test.go`. The `lll` rule in `.golangci.yml` is the real length check; the profile has no command for it, so `go test ./internal/engine/...` confirms the test still compiles and passes.
