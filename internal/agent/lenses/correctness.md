# correctness

## correctness.1

The logic does what the task card says for every input the card names. Follow each named input through the changed function. Quote the line whose result differs from the card.

## correctness.2

An error return or failure is handled, not ignored. A blank identifier on an error outside a deferred cleanup, or an empty catch block, is a finding. Quote the line that drops or swallows the error.

## correctness.3

Nil, empty and out-of-range values cannot crash the changed code. Quote the line that dereferences or indexes without a check.

## correctness.4

Files, connections, locks and processes opened by the changed code are released on every path. Check the success path and each error path. Quote the line that opens a resource with no matching release.
