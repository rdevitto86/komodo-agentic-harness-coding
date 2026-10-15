---
v: 1
name: standards-kotlin
description: "Kotlin standards: null safety, immutability and coroutines"
globs:
  - "*.kt"
markers: []
sections:
  - "Safety"
  - "Coroutines"
---
## Safety
- **Never** use the not-null assertion operator in production code.
- **Always** prefer val and immutable collections over var and mutable ones.

## Coroutines
- **Always** run a coroutine in a scope that has an owner.
- **Never** block a thread inside a suspending function.
