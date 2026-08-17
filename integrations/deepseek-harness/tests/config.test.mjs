import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";

import { normalizeConfig } from "../lib/index.js";

test("loads inertly with explicit empty opt-in paths", () => {
  const config = normalizeConfig({});
  assert.equal(config.captureDir, "");
  assert.equal(config.evidenceRoot, "");
  assert.equal(config.portableRepo, "");
});

test("requires retrieval roots as an absolute pair", () => {
  assert.throws(() => normalizeConfig({ evidenceRoot: path.resolve("evidence") }), /configured together/);
  assert.throws(() => normalizeConfig({
    evidenceRoot: "relative-evidence",
    portableRepo: path.resolve("portable"),
  }), /evidenceRoot.*absolute path/);
});

test("validates positive bounded budgets", () => {
  assert.throws(() => normalizeConfig({ limit: 0 }), /limit.*positive/);
  assert.throws(() => normalizeConfig({ stdoutMaxBytes: 100 * 1024 * 1024 }), /must not exceed/);
});
