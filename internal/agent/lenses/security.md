# security

> Needs owner sign-off: the owner reviews this rule book before it is final.

## security.1

No input reaches a shell, query or template without being escaped or passed as a separate argument. Trace each value that comes from a user, a file, the network or a model into every command line, query string and template; string joining or formatting into the text is the fault. Quote the line that builds the command, query or template from input.

## security.2

No secret, key or token appears in code, a log line, an error message or an event. Look for literals that resemble keys or passwords, and for logs, errors or events that print a credential, a header or a whole configuration. Quote the line that holds or prints the secret, without repeating the secret itself.

## security.3

A path built from input is cleaned and cannot leave its allowed folder. Check each join of an input path with a base folder for a clean step and a check that the result stays under the base, covering `..` and absolute paths. Quote the line that joins the input path without checking the result.

## security.4

A program is started only with an argument list and an explicit environment. Look for a shell string, an argument built from input, and an inherited environment that passes secrets to the child. Quote the line that starts the program.

## security.5

Network clients verify certificates and never accept any host. Look for options that skip verification, custom verifiers that always succeed, and plain HTTP to a remote host. Quote the line that turns off verification.
