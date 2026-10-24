# readiness

## readiness.1

Changed behaviour has a test that fails without the change. For each changed function, trace every branch, including branches of existing functions the diff modifies, and find a test that reaches it. The checks slot lists added lines that no test covers; each uncovered added branch is a finding unless a test covers it. Run the test with the change reverted when in doubt. Quote the changed line that no test covers.

## readiness.2

Documents and the changelog are touched when a task says so. Read each task card for a named document, then look for it in the changed files. Quote the task line that names the document the diff leaves unchanged.
