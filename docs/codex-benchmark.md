## Native Codex paired diagnostic

The fixed-population evaluator above is the release gate for continuous-learning
claims. A smaller native Codex benchmark is available for a different purpose:
checking whether an exact promoted-memory delivery changes a real local
`codex exec` result on a preregistered synthetic suite.

Create the promoted memory through the normal capture, review, promotion,
portable export, and retrieval workflow. Use the `injection_id` returned by
`recall context` in a local plan:

```json
{
  "schema_version": "codex-memory-benchmark-plan/v1alpha1",
  "suite_id": "promoted-memory-check",
  "model": "default",
  "timeout_seconds": 180,
  "tasks": [
    {
      "task_id": "stored-project-instruction",
      "cluster_id": "project-guidance",
      "prompt": "Reply with exactly the stored project instruction and nothing else.",
      "tool_policy": "forbid",
      "injection_id": "injection-<id>",
      "workspace": "workspace",
      "oracle_overlay": "oracle-overlay",
      "oracle_command": ["<trusted-local-oracle>"]
    }
  ],
  "privacy": "local_only"
}
```

Run it only in a dedicated evidence root and a new output directory:

```text
agentmem eval codex benchmark \
  --root <dedicated-local-evidence> \
  --file <plan.json> \
  --output <new-local-output-directory> \
  --codex <codex-executable> \
  --confirm-oracle-execution
```

The confirmation flag authorizes execution of every oracle command named by the
plan. The runner stores the exact input plan, sealed plan, runner, Codex, and
oracle executable bytes, workspace and oracle-overlay files, raw JSONL event streams,
agent messages, stderr, oracle output, token usage, and terminal events as local
content-addressed evidence. Oracle arguments cannot reference paths, and each
arm uses a fresh Git workspace. A `tool_policy` of `forbid` is sealed into the
plan, measured from Codex JSONL item events, and makes an arm ineligible if any
tool executes. The frozen public suite passes only an answer hash to its oracle;
it does not stage the plaintext expected answer. The environment variable names are hashed for
diagnostics; secret values are inherited for authentication and intentionally
not persisted.

`memory_context` may be used instead of `injection_id` for runner development,
but it is always classified as caller-provided diagnostic context. Only a
verified retrieval/injection receipt counts as product memory exposure.

Verify a completed report against its full local ledger and content-addressed
blobs:

```text
agentmem eval codex verify \
  --root <dedicated-local-evidence> \
  --file <report.json>
```

After verification, export a synthetic-public aggregate receipt without raw
events, messages, stderr, thread identifiers, or local paths:

```text
agentmem eval codex receipt \
  --root <dedicated-local-evidence> \
  --file <report.json> \
  --suite evals/codex-memory-v1/suite.json
```

The published `codex-memory-capability-v1` receipt covers 20 distinct clusters:
19 wins, 1 tie, 0 losses; baseline 1/20 and memory 20/20; all 40 arms emitted
zero tool calls; the one-sided sign-test value is `0.0000019073486328125`.
The exact report replayed 204 ledger records and 229 blobs without issues.
This result is an authenticated local Codex run bound to exact artifacts, but
it is not independently reproducible from the aggregate receipt.

A report remains `measurement_only` unless every pair uses verified retrieval,
there are at least 20 distinct task clusters, execution has no issue, treatment
wins exceed losses and baseline passes, and the one-sided paired sign test is at
most `0.05`. Every pair must also seal `tool_policy=forbid` and emit zero tool
calls. A passing bounded report applies only to its exact sealed suite. It
does not measure repeated future corrections, authenticate private provider
reasoning, or satisfy the stronger continuous-learning release gate.
