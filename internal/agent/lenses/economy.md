# economy

## economy.1

The correctness checks hold at reduced depth: the logic matches the card, errors are handled, empty and boundary values are checked, and resources are released. Follow each changed branch once. Quote the line that breaks the rule.

## economy.2

The contract checks hold at reduced depth: callers of changed exported symbols still work and a task intends the change. Quote the call site or declaration.

## economy.3

The concurrency checks hold at reduced depth: shared data is guarded, locks are ordered, goroutines can end. Quote the unguarded access or the goroutine start.

## economy.4

The platform checks hold at reduced depth: paths, case, line endings and signals do not assume one platform. Quote the platform-bound line.

## economy.5

The config checks hold at reduced depth: hard limits, schema versions and gate fixtures of embedded data are kept. Quote the changed line of the data file.

## economy.6

The readiness checks hold at reduced depth: changed behaviour has tests, including every added branch the checks slot lists as uncovered, and the documents a task names are touched. Quote the changed function with no test.

## economy.7

The quality checks hold at reduced depth: names, repetition and comments follow the standards of the brief. Quote the declaration that breaks the standard.
