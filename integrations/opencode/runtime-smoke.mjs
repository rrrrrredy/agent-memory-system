import { createHash, randomBytes } from "node:crypto"
import { copyFile, mkdir, mkdtemp, readFile, readdir, rm } from "node:fs/promises"
import { createServer } from "node:net"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { fileURLToPath } from "node:url"
import { spawn } from "node:child_process"

const opencode = requiredEnvironment("OPENCODE_BINARY")
const agentmem = requiredEnvironment("AGENTMEM_BINARY")
const sourceDirectory = dirname(fileURLToPath(import.meta.url))
const root = await mkdtemp(join(tmpdir(), "agentmem-opencode-runtime-"))
const project = join(root, "project")
const pluginDirectory = join(project, ".opencode", "plugins")
const spoolDirectory = join(root, "spool")
const spoolPath = join(spoolDirectory, "events.jsonl")
const evidenceRoot = join(root, "evidence")
const password = randomBytes(24).toString("hex")
const port = await availablePort()
let server

try {
  await mkdir(pluginDirectory, { recursive: true, mode: 0o700 })
  await mkdir(spoolDirectory, { recursive: true, mode: 0o700 })
  await copyFile(
    join(sourceDirectory, "agent-memory-evidence.ts"),
    join(pluginDirectory, "agent-memory-evidence.ts"),
  )
  await copyFile(
    join(sourceDirectory, "crash-safe-jsonl.mjs"),
    join(pluginDirectory, "crash-safe-jsonl.mjs"),
  )

  server = startServer({ opencode, project, spoolPath, password, port })
  const health = await waitForHealth(port, password, server)
  const baselineEventHashes = new Set((await readCapturedEvents(spoolDirectory)).map(eventHash))
  let session
  try {
    session = await requestJSON(port, password, "/session", {
      method: "POST", timeout: 20_000,
      headers: { "content-type": "application/json" },
      body: "{}",
    })
  } catch (error) {
    throw new Error(`OpenCode session creation failed: ${error.message}\n${childOutput(server)}`)
  }
  if (typeof session?.id !== "string" || session.id.length === 0) {
    throw new Error("OpenCode did not create a session")
  }

  const captured = await waitForSessionEvent(spoolDirectory, baselineEventHashes, session.id)
  await run(agentmem, ["init", "--root", evidenceRoot])
  const imported = parseJSON(await run(agentmem, [
    "import", "opencode-events", "--root", evidenceRoot, "--path", spoolDirectory,
  ]), "OpenCode import result")
  const doctor = parseJSON(await run(agentmem, ["doctor", "--root", evidenceRoot]), "doctor report")
  if (!Number.isInteger(imported.events_appended) || imported.events_appended < 1) {
    throw new Error("OpenCode events were not imported into the evidence ledger")
  }
  if (doctor.ready !== true || !Array.isArray(doctor.issues) || doctor.issues.length !== 0) {
    throw new Error("the imported OpenCode evidence store did not pass verification")
  }

  process.stdout.write(`${JSON.stringify({
    schema_version: "opencode-runtime-smoke/v1alpha1",
    runtime_version: health.version,
    native_events_captured: captured.total,
    native_session_event_type: captured.type,
    native_session_id_sha256: sha256(session.id),
    native_session_event_sha256: captured.eventSHA256,
    evidence_events_appended: imported.events_appended,
    evidence_ready: doctor.ready,
    provider_model_invoked: false,
  })}\n`)
} finally {
  await stopServer(server)
  await rm(root, { recursive: true, force: true })
}

function requiredEnvironment(name) {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required`)
  return value
}

async function availablePort() {
  const listener = createServer()
  await new Promise((resolve, reject) => {
    listener.once("error", reject)
    listener.listen(0, "127.0.0.1", resolve)
  })
  const address = listener.address()
  const port = typeof address === "object" && address ? address.port : 0
  await new Promise((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()))
  if (!Number.isInteger(port) || port < 1) throw new Error("failed to allocate a local port")
  return port
}

function startServer(options) {
  const output = []
  const child = spawn(options.opencode, [
    "serve", "--hostname", "127.0.0.1", "--port", String(options.port),
  ], {
    cwd: options.project,
    env: {
      ...process.env,
      AGENT_MEMORY_OPENCODE_EVENT_LOG: options.spoolPath,
      OPENCODE_SERVER_PASSWORD: options.password,
    },
    stdio: ["ignore", "pipe", "pipe"],
  })
  child.stdout.on("data", (chunk) => output.push(chunk.toString("utf8")))
  child.stderr.on("data", (chunk) => output.push(chunk.toString("utf8")))
  child.collectedOutput = output
  return child
}

async function waitForHealth(port, password, child) {
  const deadline = Date.now() + 45_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`OpenCode exited before health check:\n${childOutput(child)}`)
    }
    try {
      const health = await requestJSON(port, password, "/global/health")
      if (health?.healthy === true && typeof health.version === "string") return health
    } catch {
      // The server may still be starting.
    }
    await delay(250)
  }
  throw new Error(`OpenCode health check timed out:\n${childOutput(child)}`)
}

async function requestJSON(port, password, path, options = {}) {
  const { timeout = 5_000, ...requestOptions } = options
  const authorization = Buffer.from(`opencode:${password}`).toString("base64")
  const response = await fetch(`http://127.0.0.1:${port}${path}`, {
    ...requestOptions,
    headers: { ...requestOptions.headers, authorization: `Basic ${authorization}` },
    signal: AbortSignal.timeout(timeout),
  })
  const text = await response.text()
  if (!response.ok) throw new Error(`${path} returned ${response.status}: ${text}`)
  return JSON.parse(text)
}

async function waitForSessionEvent(directory, baselineEventHashes, sessionID) {
  const deadline = Date.now() + 20_000
  while (Date.now() < deadline) {
    const records = await readCapturedEvents(directory)
    for (const record of records) {
      const digest = eventHash(record)
      if (baselineEventHashes.has(digest)) continue
      if (record.event.type === "session.created" && eventSessionID(record.event) === sessionID) {
        return { total: records.length, type: record.event.type, eventSHA256: digest }
      }
    }
    await delay(200)
  }
  throw new Error("OpenCode loaded but the evidence plugin did not capture the created session")
}

async function readCapturedEvents(directory) {
  const entries = (await readdir(directory)).filter((entry) => entry.endsWith(".jsonl")).sort()
  const records = []
  for (const name of entries) {
    const data = await readFile(join(directory, name), "utf8")
    for (const line of data.split("\n")) {
      if (!line.trim()) continue
      const record = JSON.parse(line)
      if (!record.captured_at || typeof record.event !== "object" || record.event === null) {
        throw new Error("OpenCode plugin wrote an invalid event envelope")
      }
      records.push(record)
    }
  }
  return records
}

function eventSessionID(event) {
  const properties = event?.properties
  return properties?.info?.id ?? properties?.session?.id ?? properties?.id ??
    properties?.sessionID ?? properties?.sessionId ?? ""
}

function eventHash(record) {
  return sha256(JSON.stringify(record.event))
}

function sha256(value) {
  return createHash("sha256").update(value).digest("hex")
}

async function run(executable, arguments_) {
  return await new Promise((resolve, reject) => {
    const child = spawn(executable, arguments_, { stdio: ["ignore", "pipe", "pipe"] })
    const stdout = []
    const stderr = []
    child.stdout.on("data", (chunk) => stdout.push(chunk))
    child.stderr.on("data", (chunk) => stderr.push(chunk))
    child.once("error", reject)
    child.once("exit", (code) => {
      const output = Buffer.concat(stdout).toString("utf8")
      if (code === 0) resolve(output)
      else reject(new Error(`${executable} exited ${code}: ${Buffer.concat(stderr).toString("utf8")}`))
    })
  })
}

function parseJSON(value, label) {
  try {
    return JSON.parse(value)
  } catch (error) {
    throw new Error(`${label} is not JSON: ${error.message}`)
  }
}

async function stopServer(child) {
  if (!child || child.exitCode !== null) return
  child.kill("SIGTERM")
  await Promise.race([
    new Promise((resolve) => child.once("exit", resolve)),
    delay(5_000).then(() => child.kill("SIGKILL")),
  ])
}

function childOutput(child) {
  return child.collectedOutput.join("").slice(-8_000)
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
