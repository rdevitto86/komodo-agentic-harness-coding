# concurrency

## concurrency.1

Shared data is read and written under one lock or through one channel. Each variable used by two goroutines or threads is guarded. Quote the line that touches shared data without the lock.

## concurrency.2

Locks are taken in one fixed order and never held across a blocking call. Quote the line that takes the second lock in a different order, or that blocks while a lock is held.

## concurrency.3

Long work stops when its context is cancelled. Each loop and blocking call in the changed code watches the context or a stop signal. Quote the loop or call that ignores cancellation.

## concurrency.4

Every goroutine started has a way to end. It ends when its work ends or its context is cancelled. Quote the line that starts a goroutine that has no exit path.
