import assert from "node:assert/strict"
import { createHash } from "node:crypto"
import { mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import test from "node:test"

import { CrashSafeJSONLSpool } from "./crash-safe-jsonl.mjs"

const fixedTime = new Date("2026-08-04T08:00:00.000Z")

async function temporaryDirectory(t) {
  const directory = await mkdtemp(join(tmpdir(), "agent-memory-spool-"))
  t.after(() => rm(directory, { recursive: true, force: true }))
  return directory
}

test("rollover starts a new path without renaming an imported segment", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const first = { sequence: 1, value: "first" }
  const firstLine = JSON.stringify(first) + "\n"
  const spool = new CrashSafeJSONLSpool(destination, {
    maxBytes: Buffer.byteLength(firstLine), now: () => fixedTime,
    unique: () => "fixed", pid: 42,
  })

  await spool.append(first)
  const firstPath = join(directory,
    (await readdir(directory)).find((name) => name.includes(".segment-")))
  const original = await readFile(firstPath, "utf8")
  await spool.append({ sequence: 2, value: "second" })
  await spool.drain()

  assert.equal(original, firstLine)
  assert.equal(await readFile(firstPath, "utf8"), firstLine)
  const files = (await readdir(directory)).filter((name) => name.endsWith(".jsonl"))
  assert.equal(files.length, 2)
  const records = []
  for (const name of files) {
    const content = await readFile(join(directory, name), "utf8")
    assert.ok(content.endsWith("\n"))
    records.push(...content.trimEnd().split("\n").map(JSON.parse))
  }
  assert.deepEqual(records.sort((left, right) => left.sequence - right.sequence), [
    first, { sequence: 2, value: "second" },
  ])
})

test("startup preserves a partial tail before truncating its source", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const complete = JSON.stringify({ sequence: 1 }) + "\n"
  const fragment = `{"sequence":2`
  const staleName = "events.segment-p99999999-stale-0-fixed.jsonl"
  const stalePath = join(directory, staleName)
  await writeFile(stalePath, complete + fragment, { mode: 0o600 })
  const spool = new CrashSafeJSONLSpool(destination, {
    now: () => fixedTime, unique: () => "fixed", pid: process.pid,
  })

  await spool.append({ sequence: 3 })

  assert.equal(await readFile(stalePath, "utf8"), complete)
  const files = await readdir(directory)
  const recovered = files.find((name) => name.endsWith(".partial.jsonl"))
  const segment = files.find((name) => name.includes(".segment-") && name !== staleName &&
    !name.endsWith(".partial.jsonl"))
  assert.ok(recovered)
  assert.ok(segment)
  assert.equal(await readFile(join(directory, recovered), "utf8"), fragment)
  assert.deepEqual(JSON.parse((await readFile(join(directory, segment), "utf8")).trim()),
    { sequence: 3 })

  const secondStartup = new CrashSafeJSONLSpool(destination)
  await secondStartup.initialize()
  const afterRestart = await readdir(directory)
  assert.equal(afterRestart.filter((name) => name.endsWith(".partial.jsonl")).length, 1)
  assert.equal(await readFile(join(directory, recovered), "utf8"), fragment)
})

test("concurrent callers are serialized into complete JSONL records", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const spool = new CrashSafeJSONLSpool(destination)

  await Promise.all(Array.from({ length: 40 }, (_, sequence) => spool.append({ sequence })))
  const segment = (await readdir(directory)).find((name) => name.includes(".segment-"))
  const content = await readFile(join(directory, segment), "utf8")
  assert.ok(content.endsWith("\n"))
  const records = content.trimEnd().split("\n").map(JSON.parse)
  assert.equal(records.length, 40)
  assert.deepEqual(records.map((record) => record.sequence),
    Array.from({ length: 40 }, (_, sequence) => sequence))
})

test("a serialization failure does not poison later appends", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const spool = new CrashSafeJSONLSpool(destination)

  await assert.rejects(spool.append({ unsupported: 1n }))
  await spool.append({ sequence: 1 })
  const segment = (await readdir(directory)).find((name) => name.includes(".segment-"))
  assert.deepEqual(JSON.parse((await readFile(join(directory, segment), "utf8")).trim()),
    { sequence: 1 })
})

test("startup does not repair a segment owned by a live process", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const activeName = `events.segment-p${process.pid}-active-0-fixed.jsonl`
  const activePath = join(directory, activeName)
  const fragment = `{"sequence":1`
  await writeFile(activePath, fragment, { mode: 0o600 })

  const spool = new CrashSafeJSONLSpool(destination)
  await spool.initialize()
  assert.equal(await readFile(activePath, "utf8"), fragment)
  assert.equal((await readdir(directory)).filter((name) =>
    name.endsWith(".partial.jsonl")).length, 0)
})

test("a recovery conflict is reported without blocking a new segment", async (t) => {
  const directory = await temporaryDirectory(t)
  const destination = join(directory, "events.jsonl")
  const staleName = "events.segment-p99999999-conflict-0-fixed.jsonl"
  const stalePath = join(directory, staleName)
  const complete = JSON.stringify({ sequence: 1 }) + "\n"
  const fragment = `{"sequence":2`
  const digest = createHash("sha256").update(fragment).digest("hex")
  const stem = staleName.slice(0, -".jsonl".length)
  await writeFile(stalePath, complete + fragment, { mode: 0o600 })
  await writeFile(join(directory, `${stem}.recovered-${digest}.partial.jsonl`),
    "conflicting recovery bytes", { mode: 0o600 })
  const failures = []
  const spool = new CrashSafeJSONLSpool(destination, {
    onRecoveryError: (path, error) => failures.push({ path, error }),
  })

  await spool.append({ sequence: 3 })
  assert.equal(failures.length, 1)
  assert.equal(failures[0].path, stalePath)
  assert.match(failures[0].error.message, /does not match its digest/)
  assert.equal(await readFile(stalePath, "utf8"), complete + fragment)
  const newSegment = (await readdir(directory)).find((name) =>
    name.includes(".segment-") && name !== staleName && !name.endsWith(".partial.jsonl"))
  assert.deepEqual(JSON.parse((await readFile(join(directory, newSegment), "utf8")).trim()),
    { sequence: 3 })
})

test("a spool inside a Git worktree is rejected", async (t) => {
  const directory = await temporaryDirectory(t)
  await mkdir(join(directory, ".git"))
  const spool = new CrashSafeJSONLSpool(join(directory, "raw", "events.jsonl"))
  await assert.rejects(spool.append({ sequence: 1 }), /outside a Git worktree/)
})

test("a symlink into a Git worktree is rejected", async (t) => {
  const directory = await temporaryDirectory(t)
  const repository = join(directory, "repository")
  const data = join(repository, "data")
  const alias = join(directory, "alias")
  await mkdir(join(repository, ".git"), { recursive: true })
  await mkdir(data)
  try {
    await symlink(data, alias, process.platform === "win32" ? "junction" : "dir")
  } catch (error) {
    if (["EPERM", "EACCES", "UNKNOWN"].includes(error?.code)) {
      t.skip("directory links are unavailable")
      return
    }
    throw error
  }
  const spool = new CrashSafeJSONLSpool(join(alias, "events.jsonl"))
  await assert.rejects(spool.append({ sequence: 1 }), /outside a Git worktree/)
})
