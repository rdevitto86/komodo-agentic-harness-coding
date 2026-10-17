---
v: 1
name: standards-go
description: "Go standards: errors, naming, tests, formatting and file layout"
globs:
  - "*.go"
markers: []
sections:
  - "Errors"
  - "Naming"
  - "Tests"
  - "Formatting"
  - "File layout"
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

## Formatting
- **Always** wrap a line at 100 columns, counting a tab as 4, in code and in comments.
- **Always** wrap a call, a signature or a struct literal that does not fit as one item per line, aligned.
- **Always** put a blank line before a return, after an error check, and around a defer, switch, for or select block.
- **Always** separate the steps of a function with a blank line.
- **Always** group constants and variables into const and var blocks, one block per purpose.
- **Never** leave a table, map or compiled pattern inside a function when it never changes; declare it once at package level.
- **Always** declare a constant inside the one function that uses it.

## File layout
- **Always** order a file as types and interfaces, then constants and variables, then New functions, then methods, then plain functions.
- **Always** put public declarations before private ones within each group.
- **Always** keep a constant or variable beside the type it belongs to when it describes that type.
