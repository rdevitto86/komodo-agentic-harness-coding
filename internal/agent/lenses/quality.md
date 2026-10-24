# quality

## quality.1

Names say what the thing is or does. Look for single letters outside short loops, names that restate the type, names that mean something other than what the code does, and abbreviations a new reader cannot decode. Quote the line that declares a vague, empty or misleading name.

## quality.2

The same code is not written twice. Look for blocks of 4 or more lines that repeat inside the diff or repeat code that already exists nearby. Quote the first line of the second copy.

## quality.3

Comments follow the comments standard of the brief. Check each added or changed function and field for a comment, its length limit and whether it says what the code does. Test files and test helpers may go without a comment. Quote the function or field that lacks the comment or breaks the standard.
