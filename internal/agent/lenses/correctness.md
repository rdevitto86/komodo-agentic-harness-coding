# correctness

## correctness.1

The logic does what the task card says for every input the card names. List the inputs and outputs the card states, then follow each one through the changed function, taking every branch. Run the card's example when a command exists. Quote the line whose result differs from the card.

## correctness.2

An error return or failure is handled, not ignored. Look for a blank identifier on an error outside a deferred cleanup, an empty catch or except block, a result that is never checked, and an error logged but then treated as success. Quote the line that drops or swallows the error.

## correctness.3

Nil, empty and out-of-range values cannot crash the changed code. For each parameter, map entry, slice index and returned pointer, ask what happens when it is nil, empty, zero or the last element, and look for the check. Quote the line that dereferences or indexes without a check.

## correctness.4

Files, connections, locks and processes opened by the changed code are released on every path. Check the success path and each error path, including early returns and panics. Quote the line that opens a resource with no matching release.

## correctness.5

Boundary values are handled exactly. At every limit, count, index, size or time comparison, check for an off-by-one and for inclusive versus exclusive use: `<` against `<=`, a first or last element, the value equal to the limit, one below and one above. Compare each boundary with what the card or the documented limit says. Quote the comparison line.

## correctness.6

A partial failure cannot duplicate or lose data. Where a retry, requeue or resume follows a partly finished write, send, charge or state change, check what the second attempt repeats and what the first attempt left behind. A step is safe to repeat only when it is idempotent or records its progress. Quote the retry or requeue line.
