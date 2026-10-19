# security

> Needs owner sign-off: the owner reviews this rule book before it is final.

## security.1

No input reaches a shell, query or template without being escaped or passed as separate arguments. Input that reaches a command line, a query or a template is passed as a separate argument or escaped. Quote the line that builds the command, query or template from input.

## security.2

No secret, key or token is written into code, logs or events. No credential appears in source, a log line, an error message or an event. Quote the line that holds or prints the secret.

## security.3

A path built from input cannot leave its allowed folder. A path that includes input is cleaned and checked to stay inside its folder. Quote the line that joins the input path without checking the result.

## security.4

A program is started only with fixed arguments and a controlled environment. A started program receives its arguments as a list and an explicit environment. Quote the line that starts the program.

## security.5

Network clients verify certificates and do not disable TLS checks. No client skips certificate verification or accepts any host. Quote the line that turns off verification.
