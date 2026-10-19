# economy

## economy.1

Merged checklist of the correctness lens at reduced depth: the logic matches the task card, errors are handled, empty values are checked and resources are released. Quote the line that breaks the rule.

## economy.2

Merged checklist of the contract lens at reduced depth: callers of changed exported symbols still work and a task intends the change. Quote the call site or declaration.

## economy.3

Merged checklist of the concurrency lens at reduced depth: shared data is guarded, locks are ordered and goroutines can end. Quote the unguarded access or the goroutine start.

## economy.4

Merged checklist of the platform lens at reduced depth: paths, case, line endings and signals do not assume one platform. Quote the platform-bound line.

## economy.5

Merged checklist of the config lens at reduced depth: hard limits, schema versions and gate fixtures of embedded data are kept. Quote the changed line of the data file.

## economy.6

Merged checklist of the readiness lens at reduced depth: changed behaviour has tests and the documents a task names are touched. Quote the changed function with no test.

## economy.7

Merged checklist of the quality lens at reduced depth: names, repetition and comments follow the standards of the brief. Quote the declaration that breaks the standard.
