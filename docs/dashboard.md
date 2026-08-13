# Local read-only dashboard

The dashboard is a small operational view over verified local state. It shows
ledger readiness, active-memory and loadout counts, native execution receipts,
prospective study progress, and verification issues.

```text
agentmem serve dashboard \
  --root <local-evidence-directory> \
  --repo <portable-memory-directory>
```

Open `http://127.0.0.1:8765`. Use `--listen 127.0.0.1:<port>` to choose another
loopback port.

## Privacy and write boundary

The server refuses non-loopback listen addresses and rejects requests whose
`Host` header is not an exact localhost or loopback IP, which blocks ordinary
DNS-rebinding access. Its HTML has no external scripts,
fonts, images, analytics, or network dependencies. The JSON endpoint and page
serve summary metadata only. They do not serve:

- raw transcripts or normalized message text;
- tool payloads or file contents;
- exposed or summarized reasoning;
- promoted memory text;
- prompts, native Agent output, secrets, or local source paths.

`GET /api/status` returns the versioned
`agent-memory-dashboard-snapshot/v1alpha1` envelope. The root page supports
only `GET` and `HEAD`; write methods return `405 Method Not Allowed`. Responses
set no-store and restrictive browser security headers.

Loopback is a network-exposure control, not user authentication. Another
process running as the same local user may still reach the endpoint. Stop the
server when it is not needed, and do not expose it through a proxy, tunnel, or
port-forwarding rule.

## Readiness meaning

`ready=true` means the supported local verification reports and the complete
portable repository have no current integrity issue. It does not mean:

- every Agent runtime is installed or authenticated;
- a provider executed a model;
- a caller attestation came from an authenticated person;
- a memory was useful; or
- longitudinal efficacy has been certified.

The dashboard refreshes every 15 seconds by rebuilding read-only verification
reports. It does not approve candidates, promote memory, start sync, run an
Agent, or append study observations.
