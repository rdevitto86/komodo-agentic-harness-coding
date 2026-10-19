# concurrency

## concurrency.1

Shared data is read and written under one lock or through one channel. For each variable, map or field reachable from more than one goroutine or thread, find the lock or channel that guards every read and write; a lock on the write alone is a fault. Run the package tests with the race detector when the language has one. Quote the line that touches shared data without the lock.

## concurrency.2

Locks are taken in one fixed order and never held across a blocking call. Look for two locks taken in opposite orders on different paths, and for a lock held over a channel send, a network call, a file read or a sleep. Quote the line that takes the second lock out of order, or blocks while holding one.

## concurrency.3

Long work stops when its context is cancelled. Check each loop and blocking call in the changed code for a cancellation or timeout check and for a context passed on to callees. Quote the loop or call that ignores cancellation.

## concurrency.4

Every goroutine started has a way to end. For each goroutine, thread or task started, find the channel close, cancellation or return that ends it, including when its reader goes away. Quote the line that starts a goroutine with no exit path.
