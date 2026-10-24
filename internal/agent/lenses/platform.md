# platform

## platform.1

Paths use the path package's separator functions, not a typed slash. Look for string joining of path parts with `/` or a backslash, and for splitting a path on a typed separator. Quote the line that builds or splits a path with a typed separator.

## platform.2

File names are not assumed to be case-sensitive or case-insensitive. Look for comparisons of names with a fixed case, two names that differ only by case, and lookups that rely on one file system's behaviour. Quote the line that compares or builds names by case.

## platform.3

Text read from or written to files does not assume one line ending. Look for splits or joins on a bare newline, trims of one character, and carriage returns left on the end of a line. Quote the line that splits or joins on a fixed line ending.

## platform.4

Signals and process groups are used only where the platform supports them. A call that exists on one platform only sits in a file built for that platform. Check the file's build tag or name against the call. Quote the line that uses a platform-only signal or call.
