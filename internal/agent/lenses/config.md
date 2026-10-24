# config

## config.1

Hard limits stay at their stated values. Compare each changed constant, default or limit in a configuration or data file with the value the card, the design or a neighbouring document states. Quote the line that changes a hard limit.

## config.2

The schema version is raised when the shape of the data changes. When a field is added, renamed, removed or retyped in a data file or its schema, look for a changed version field and for a migration of existing data. Quote the changed field whose version line is unchanged.

## config.3

The gate fixture matches the changed policy. A changed or added policy row has a fixture case that proves it, and no fixture case describes a removed row. Quote the policy row that has no matching fixture case.
