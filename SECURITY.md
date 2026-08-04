# Security policy

## Never publish raw evidence

Raw transcripts, tool outputs, reasoning artifacts, credentials, personal
information, and local paths can be sensitive. Do not include them in issues,
pull requests, test fixtures, screenshots, or the public code repository.

Use synthetic fixtures for reports. If a bug can only be reproduced with real
evidence, reduce and redact it locally first.

## Reporting a vulnerability

Open a GitHub security advisory when available. Do not open a public issue that
contains a secret or a sample of private evidence.

If evidence is accidentally committed:

1. revoke exposed credentials immediately;
2. stop synchronization;
3. remove the data from repository history;
4. verify every clone and backup;
5. record the incident using redacted metadata only.
