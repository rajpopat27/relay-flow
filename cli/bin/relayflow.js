#!/usr/bin/env node
const { spawnSync } = require("node:child_process")
const fs = require("node:fs")
const os = require("node:os")
const path = require("node:path")

const root = path.dirname(__dirname)
const binDir = path.join(os.tmpdir(), "relayflow", "bin")
const bin = path.join(binDir, "relayflow")

if (!fs.existsSync(bin)) {
  fs.mkdirSync(binDir, { recursive: true })
  const build = spawnSync("go", ["build", "-o", bin, path.join(root, "cmd", "relayflow")], {
    cwd: root,
    stdio: "inherit",
  })
  if (build.status !== 0) process.exit(build.status ?? 1)
}

const run = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" })
process.exit(run.status ?? 0)
