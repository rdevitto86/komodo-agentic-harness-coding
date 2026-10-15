# correctness

## correctness.1

Read each changed function against the card. A branch that returns the wrong result for an input the card names is a finding; quote that line.

## correctness.2

Every error a changed call can return is checked or passed on. A blank identifier on an error, or an empty catch block, is a finding; quote that line.

## correctness.3

A pointer, map, slice index or optional value is checked before use where the changed code can receive an empty or missing one. Quote the unchecked use.

## correctness.4

Each opened resource has a release on the success path and on every error path. Quote the line that opens the resource that is never released.
