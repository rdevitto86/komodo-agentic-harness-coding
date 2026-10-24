---
v: 1
name: standards-go
description: "Go standards: errors, naming, tests, line width, wrapping, blank lines, declarations and file layout"
globs:
  - "*.go"
markers: []
sections:
  - "Errors"
  - "Naming"
  - "Tests"
  - "Line width"
  - "Wrapping"
  - "Blank lines"
  - "Declarations"
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

## Line width
Widths count a tab as 4. A body is the lines between its braces; a nested body is one inside a for, range, if, case, select or function literal.
- **Always** keep a code line and a trailing comment within 125 columns.
- **Always** wrap a doc comment, and a comment above a declaration or field, at the last space within 125 columns.
- **Never** break a line that fits, except where Wrapping gives a count.

## Wrapping
- **Always** break a call that passes the limit after its opening parenthesis, one argument per line, each ending in a comma, with the closing parenthesis alone on the last line.
- **Always** open a last-argument composite literal or function literal on the call line and close it with `})`; its own fields or statements take one line each.
- **Always** break a signature of 4 or more parameter declarations one declaration per line when it passes the limit; names that share a type, such as `common, epic string`, are one declaration.
- **Always** break 4 or more results one per line inside their own parentheses when the line that holds them passes the limit.
- **Always** put a keyed literal of 5 or more fields one field per line, whatever its width; a shorter one stays on one line while it fits.
- **Always** break a long condition or sum after its operator, never before, and indent each level of parentheses one tab.
- **Always** keep a function of one simple statement on its signature line when the line stays within 100 columns; otherwise give it a body.

```go
func (c checker) coverageStep(
	ctx context.Context,
	run CommandRunner,
	in CheckInput,
	blocked bool,
) (StepResult, error) {
	schema, err := json.Marshal(map[string]any{
		"$id":  resultID,
		"type": "object",
	})
```

## Blank lines
Compact means no blank line at all. The step rules below apply to a body of 11 lines or more, function or nested.
- **Never** put a blank line in a body of 10 lines or fewer, function or nested.
- **Never** put a blank line between case clauses, after an opening brace or before a closing brace.
- **Always** keep a call on one line against its error check, and a variable declaration against the first statement that uses it.
- **Always** put a blank line after a multi-line statement when another statement follows, unless the next is an if guard of 10 lines or fewer and the statement above is 10 lines or fewer.
- **Never** put a blank line before a break, continue or goto.
- **Always** put a blank line before a for, range, switch or select that follows single-line statements, unless the line above declares the variable the statement uses.
- **Always** keep a defer against the line that acquires what it releases, and put a blank line after it.
- **Always** put a blank line before a final return that follows a multi-line statement; a return that follows a single-line statement stays against it.
- **Always** keep a comment against the statement it describes, with any blank line before the comment.
- **Never** add or remove a blank line between single-line statements that the author placed between two steps.

## Declarations
- **Never** hold a string of more than one line, such as CSS, HTML, JSON or SQL, in a Go string; keep it in a file beside the code and load it with `//go:embed`.
- **Always** group 3 or more consecutive const or var declarations in one parenthesised block, at package level and inside a function.
- **Always** declare a constant used by one function inside that function, with its comment above.
- **Never** leave a table, map or compiled pattern inside a function when it never changes; declare it once at package level.
- **Always** keep related variables together, directly above the loop or statement that uses them.
- **Always** give one comment style to a whole const, var or struct block: trailing comments aligned while every line stays within 125 columns, otherwise every entry takes its comment above.
- **Always** leave no blank line between single-line entries that take comments above; a multi-line entry has a blank line either side.

## File layout
- **Always** order the functions of a file as constructors, then public methods, then private methods, then public plain functions, then private plain functions.
- **Always** keep a public New before a private new, each right after its type.
- **Always** place a type with its constants, variables and methods as one section; a file with clear sections may put each type beside its own functions, and its plain functions after the last section.
- **Always** put a caller before the functions it calls within one group.
- **Never** move a function above its group because another function uses it.
