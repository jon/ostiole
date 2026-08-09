# Security

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability. Use GitHub's
private vulnerability-reporting form when it is available. Otherwise, email
[jon@jon.dev](mailto:jon@jon.dev) with the affected revision, enough detail to
reproduce or understand the issue, and any relevant host or hardware context.

Do not include live credentials, private keys, or other people's sensitive
data in a report. Jon will coordinate disclosure and any necessary fix with
the reporter.

## Supported versions

Ostiole is experimental and does not yet publish releases. Security fixes are
made on `main`; older revisions are not separately supported.

Hardware-access bugs can have effects beyond the host process. Include any
observed reset, halt, memory write, persistent change, adapter contention, or
failure to restore device state in the report.
