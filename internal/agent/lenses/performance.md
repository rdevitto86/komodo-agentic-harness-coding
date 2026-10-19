# performance

## performance.1

Maps, slices, queues, caches and logs that grow while the program runs also shrink, or have a fixed cap. Find each collection the changed code adds to, then look for the delete, eviction, reset or size check that bounds it; a long-running process that only appends is a leak. Quote the line that adds to the collection with no removal or cap.

## performance.2

A request or call reads or writes only the part of a file or table it needs. Look for a whole-file read, a full directory scan, a table scan with no key or limit, or a rewrite of the whole file for one record, on a path that runs per request or per call. Quote the line that loads or rewrites the whole file or table inside that path.

## performance.3

Expensive work is not repeated inside a loop when one pass outside the loop gives the same result. Look for parsing, compiling a pattern, a query, a file or network call, or a scan of a list inside a loop body, and for loops nested over the same data (quadratic cost). Quote the line inside the loop that does the repeated work.

## performance.4

Work a request can drive has limits on input size, time and concurrency. Check each read of a request body or file, each wait on a network call or lock, and each goroutine, thread or task started per item. A missing size cap, timeout or worker limit is a finding. Quote the line that reads, waits or fans out with no limit.
