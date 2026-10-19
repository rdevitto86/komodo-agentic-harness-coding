---
v: 1
name: planning
description: "Planning: ordering dependent task groups, waves, chain length and splitting a group"
globs: []
markers: []
sections:
  - "Dependencies"
  - "Waves"
  - "Chain"
  - "Splitting"
---
## Dependencies
A dependency says one group needs another group's result before it can start. Write one only for that reason.
- Name the group that creates what this group uses: a type, an interface, a file, a command or a table. This group depends on it.
- Two groups whose `touches` patterns match the same file must be ordered, directly or through a third group. Lint fails an unordered pair and names both groups and the file.
- Order by need, not by habit. A dependency written "to be safe" turns work that could run together into a chain.
- Put the group that others need first. Give it acceptance items a command can rerun, so every dependent starts from work already checked.
- Write a group with no dependency as `dependencies:` with no value.

## Waves
A wave is a set of groups that can run together. Wave 1 holds the groups with no dependency; any other group is in the wave after the latest group it waits on.
- Groups in one wave share no file in `files:`. Lint fails a draft where they do.
- Give each group of a wave files of its own. When two groups need the same file, order one after the other.
- Put each group in the earliest wave its dependencies allow.

## Chain
The chain is the longest run of groups that each wait on the one before. It sets the least time the plan can take, however many groups run together.
- A chain of over 4 groups warns. Shorten it by removing a dependency that is not needed, or by merging two small groups that always change together.
- A group with over 5 dependencies warns. It is usually two groups written as one.
- A group that every other group waits on warns, in a plan of 3 or more groups. Split out the part the others need and let the rest run later.
- A dependency cycle fails. Break it by moving what both groups need into a new group that comes first.

## Splitting
A group is sized so one builder finishes it within 1 hour: at most 8 tasks and 8 points.
- Points use the scale 1, 2, 3, 5 and 8. They compare groups with each other, so size a group against the other groups of the plan.
- Split a group over a limit into two ordered groups. Each delivers something its own acceptance items can check.
- Split along the files, so the two groups share none of them in the same wave.
- Keep a task to at most 5 files and 300 estimated changed lines. A task over either limit becomes two tasks.
