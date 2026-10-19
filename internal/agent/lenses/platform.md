# platform

## platform.1

Paths use the path package's separator functions, not a typed slash. Quote the line that builds a path with a typed separator.

## platform.2

File names are not assumed to be case-sensitive or case-insensitive. Quote the line that compares or builds names by case.

## platform.3

Text read from or written to files does not assume one line ending. Quote the line that splits or joins on a fixed line ending.

## platform.4

Signals and process groups are used only where the platform supports them. A call that exists on one platform only sits in a file for that platform. Quote the line that uses a platform-only signal or call.
