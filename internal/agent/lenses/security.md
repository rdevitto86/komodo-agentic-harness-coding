# security

## security.1

Input that reaches a command line, query or template is passed as a separate argument or escaped. Quote the line that joins input into text that is then run.

## security.2

No credential appears in source, a log line, an error message or an event. Quote the line that contains or prints it.

## security.3

A path that includes input is cleaned and checked to stay inside its folder. Quote the line that joins it without that check.

## security.4

A started program receives its arguments as a list and an explicit environment. Quote the line that starts it with input-built arguments.

## security.5

No client skips certificate verification or accepts any host. Quote the line that disables it.
