# concurrency

## concurrency.1

Shared data is read and written under one lock or through one channel. Quote the line that touches shared data without the lock.

## concurrency.2

Locks are taken in one fixed order and never held across a blocking call. Quote the line that takes the second lock out of order, or blocks while holding one.

## concurrency.3

Long work stops when its context is cancelled. Check each loop and blocking call in the changed code. Quote the loop or call that ignores cancellation.

## concurrency.4

Every goroutine started has a way to end. Quote the line that starts a goroutine with no exit path.
