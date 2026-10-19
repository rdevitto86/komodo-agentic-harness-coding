# platform

## platform.1

Paths use the platform separator functions, not a typed slash. Paths are joined with the path package functions. Quote the line that builds a path with a typed separator.

## platform.2

File names are not assumed to be case-sensitive or case-insensitive. Code does not depend on the letter case of a file name. Quote the line that compares or builds names by case.

## platform.3

Text read from or written to files does not assume one line ending. Line splitting accepts both line endings. Quote the line that splits or joins on a fixed line ending.

## platform.4

Signals and process groups are used only where the platform supports them. A signal or process call that exists on only one platform sits in a file for that platform. Quote the line that uses a platform-only signal or call.
