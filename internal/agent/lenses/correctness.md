# correctness

## correctness.1

The logic does what the task card says for every input the card names. Read each changed function against the card, then follow each input the card names through the code. A branch that returns a result different from the card's is a finding. Quote the line whose result differs from the card.

## correctness.2

An error return or failure is handled, not ignored. Every error a changed call can return is checked or passed on. A blank identifier on an error outside a deferred cleanup, or an empty catch block, is a finding. Quote the line that drops or swallows the error.

## correctness.3

Nil, empty and out-of-range values cannot crash the changed code. A pointer, map, slice index or optional value is checked before use wherever the changed code can receive an empty or missing one. Quote the line that dereferences or indexes without a check.

## correctness.4

Files, connections, locks and processes opened by the changed code are released on every path. Each opened resource has a release on the success path and on every error path. Quote the line that opens a resource with no matching release.
