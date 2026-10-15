---
v: 1
name: standards-go
description: "Go standards: errors, naming, tests and package layout"
globs:
  - "*.go"
markers: []
sections:
  - "Errors"
  - "Naming"
  - "Tests"
---
## Errors
- **Always** wrap an error with %w and a message that describes the failure.
- **Never** start an error string with the name of the function.
- **Never** discard an error with the blank identifier outside a deferred cleanup.

## Naming
- **Never** name a package util, common or helpers.
- **Always** declare a sentinel error once at package level, named ErrSomething.

## Tests
- **Always** write the failing test before the fix.
- **Always** keep tests beside their source and run them with the race detector.
