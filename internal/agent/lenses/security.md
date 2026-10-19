# security

> Needs owner sign-off: the owner reviews this rule book before it is final.

## security.1

No input reaches a shell, query or template without being escaped or passed as a separate argument. Quote the line that builds the command, query or template from input.

## security.2

No secret, key or token appears in code, a log line, an error message or an event. Quote the line that holds or prints the secret.

## security.3

A path built from input is cleaned and cannot leave its allowed folder. Quote the line that joins the input path without checking the result.

## security.4

A program is started only with an argument list and an explicit environment. Quote the line that starts the program.

## security.5

Network clients verify certificates and never accept any host. Quote the line that turns off verification.
