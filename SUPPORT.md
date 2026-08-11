# Support

## Questions and bug reports

Use GitHub Issues for reproducible product bugs and compatibility reports.
Before filing an issue, run:

```text
agentmem version
agentmem compatibility
agentmem doctor --root <local-evidence-directory>
```

Include versions, operating system, the failing command, expected behavior, and
redacted error text. Do not include a transcript, raw event, memory text, local
path, credential, or private repository URL.

A useful adapter report states only:

- Agent and version;
- operating system;
- source format category;
- whether the source was discovered;
- event counts and gap counts;
- a synthetic reproduction if available.

## Data recovery

Stop writers before recovery. Work on a copy when possible. Verify an encrypted
backup before restoring it, and restore only to a new empty target. Do not
remove a writer lock unless every process using that evidence root has stopped.

## Security issues

For vulnerabilities or accidental sensitive-data exposure, follow
[SECURITY.md](SECURITY.md). Do not open a public issue containing the affected
data.

## Compatibility expectations

Agent runtime formats are not controlled by this project. Compatibility is
reported at separate levels for fixture parsing, executable startup, native
event capture, and provider-backed model execution. See
[docs/compatibility.md](docs/compatibility.md).
