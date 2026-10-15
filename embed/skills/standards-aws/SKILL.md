---
v: 1
name: standards-aws
description: "AWS standards: identity, networking and data"
globs:
  - "*.tf"
markers:
  - "*.tf:provider \"aws\""
sections:
  - "Identity"
  - "Networking"
  - "Data"
---
## Identity
- **Always** grant a role the least permissions that let it work.
- **Never** put an access key in code or in a variable file.

## Networking
- **Never** open a security group to 0.0.0.0/0 except for a public load balancer.

## Data
- **Always** encrypt storage at rest and turn on versioning for buckets that hold state.
