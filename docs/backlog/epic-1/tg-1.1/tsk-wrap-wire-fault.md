files: cmd/komodo/wire.go
done_when: go vet ./cmd/komodo/...
estimate_lines: 10

In `cmd/komodo/wire.go`, function `runEngine`, line 226 is a `fault.Fault{...}` keyed literal of 5 fields passed as the last argument to `s.faults.Final(...)`; with tab width 4 it runs past 125 columns.

Rewrite only that literal with one field per line, each ending in a comma, and the closing brace followed by the comma that ends the argument:

```go
fault.Fault{
	Source:   src,
	Code:     "internal",
	Kind:     fault.KindInternal,
	Severity: fault.SeverityError,
	Reason:   err.Error(),
},
```

Keep the field order and values exactly as they are. Run `gofmt -w cmd/komodo/wire.go` so alignment matches gofmt. Change no other line. The `lll` rule in `.golangci.yml` (125 columns, tab 4) is the real length check; the profile has no command for it, so `go vet` confirms the file still compiles and vets clean.
