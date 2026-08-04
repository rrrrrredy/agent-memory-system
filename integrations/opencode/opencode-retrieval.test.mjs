import assert from "node:assert/strict"
import test from "node:test"

import {
  buildArguments,
  createOpenCodeRetrievalHooks,
  extractPrompt,
  OpenCodeMemoryBridge,
  retrievalConfigFromEnv,
} from "./opencode-retrieval.mjs"

test("configuration is opt-in and keeps logical scopes explicit", () => {
  assert.equal(retrievalConfigFromEnv({}), null)
  const config = retrievalConfigFromEnv({
    AGENT_MEMORY_EVIDENCE_ROOT: "D:/evidence",
    AGENT_MEMORY_PORTABLE_REPO: "D:/memory",
    AGENT_MEMORY_SCOPE_PROJECT: "project-a",
    AGENT_MEMORY_RETRIEVAL_LIMIT: "7",
  })
  assert.equal(config.binary, "agentmem")
	assert.equal(config.limit, 7)
  assert.deepEqual(buildArguments(config), [
    "inject", "opencode",
    "--root", "D:/evidence",
    "--repo", "D:/memory",
    "--limit", "7",
    "--token-budget", "600",
    "--byte-budget", "3072",
		"--scope-project", "project-a",
	])
  assert.equal(retrievalConfigFromEnv({
    AGENT_MEMORY_EVIDENCE_ROOT: "D:/evidence",
    AGENT_MEMORY_PORTABLE_REPO: "D:/memory",
    AGENT_MEMORY_RETRIEVAL_LIMIT: "7items",
  }).limit, 5)
})

test("prompt extraction ignores synthetic and non-text parts", () => {
  assert.equal(extractPrompt([
    { type: "text", text: " first instruction " },
    { type: "tool", state: {} },
    { type: "text", text: "automatic continue", synthetic: true },
    { type: "text", text: "second instruction" },
  ]), "first instruction\n\nsecond instruction")
})

test("bridge sends normalized input without allowing runtime scope overrides", async () => {
  let invocation
  const bridge = new OpenCodeMemoryBridge({
    binary: "agentmem-test",
    evidenceRoot: "D:/evidence",
    portableRoot: "D:/memory",
    repository: "trusted-repository",
    project: "",
    task: "",
    limit: 5,
    tokenBudget: 600,
    byteBudget: 3072,
    timeoutMs: 1000,
    maxOutputBytes: 4096,
  }, {
    run: async (request) => {
      invocation = request
      return JSON.stringify({
        continue: true,
        hookSpecificOutput: { additionalContext: "verified memory" },
      })
    },
  })
  const context = await bridge.retrieve({
    sessionID: "session-1",
    messageID: "message-1",
    prompt: "current task",
    cwd: "D:/work",
    source: "chat.message",
  })
  assert.equal(context, "verified memory")
  assert.deepEqual(invocation.input, {
    session_id: "session-1",
    cwd: "D:/work",
    hook_event_name: "UserPromptSubmit",
    turn_id: "message-1",
    prompt: "current task",
    source: "chat.message",
  })
  assert.deepEqual(invocation.args.slice(-2), [
    "--scope-repository", "trusted-repository",
  ])
})

test("plugin hooks receipt each actual injection and refresh it for compaction", async () => {
  const calls = []
  const bridge = {
    async retrieve(request) {
      calls.push(request)
      return request.eventName === "SessionCompacting"
        ? "compaction memory"
        : "turn memory"
    },
  }
  const hooks = createOpenCodeRetrievalHooks({ bridge, directory: "D:/work" })
  await hooks["chat.message"](
    { sessionID: "session-1", messageID: "message-1" },
    { parts: [{ type: "text", text: "keep the constraint" }] },
  )
  await hooks["chat.message"](
    { sessionID: "session-1", messageID: "synthetic-message" },
    { parts: [{ type: "text", text: "automatic continue", synthetic: true }] },
  )
  const firstSystem = []
  await hooks["experimental.chat.system.transform"](
    { sessionID: "session-1" },
    { system: firstSystem },
  )
  const secondSystem = []
  await hooks["experimental.chat.system.transform"](
    { sessionID: "session-1" },
    { system: secondSystem },
  )
  const compacting = []
  await hooks["experimental.session.compacting"](
    { sessionID: "session-1" },
    { context: compacting },
  )
  assert.deepEqual(firstSystem, ["turn memory"])
  assert.deepEqual(secondSystem, ["turn memory"])
  assert.deepEqual(compacting, ["compaction memory"])
  assert.equal(calls.length, 3)
  assert.equal(calls[0].messageID, "message-1")
  assert.equal(calls[2].prompt, "keep the constraint")
  assert.equal(calls[2].eventName, "SessionCompacting")
})

test("bridge fails open when retrieval and error reporting fail", async () => {
  const errors = []
  const bridge = new OpenCodeMemoryBridge({
    binary: "missing",
    evidenceRoot: "D:/evidence",
    portableRoot: "D:/memory",
  }, {
    run: async () => { throw new Error("not found") },
    onError: async (error) => {
      errors.push(error)
      throw new Error("logger unavailable")
    },
  })
  assert.equal(await bridge.retrieve({
    sessionID: "session-1",
    prompt: "continue safely",
  }), "")
  assert.equal(errors.length, 1)
})
