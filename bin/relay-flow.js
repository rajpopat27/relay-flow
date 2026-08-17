#!/usr/bin/env node
// relay-flow npm wrapper: downloads the prebuilt Go binary for this
// platform from GitHub Releases (tag v<version>), falling back to
// `go build` from the packaged source when no release asset matches
// or the network is unavailable. Cached in os.tmpdir()/relay-flow/bin.
const { spawnSync } = require("node:child_process")
const fs = require("node:fs")
const os = require("node:os")
const path = require("node:path")

const pkg = require(path.join(__dirname, "..", "package.json"))
const version = pkg.version
const root = path.dirname(__dirname)
const binDir = path.join(os.tmpdir(), "relay-flow", "bin")
const isWin = process.platform === "win32"
const bin = path.join(binDir, isWin ? "relay-flow.exe" : "relay-flow")

const goos = { linux: "linux", darwin: "darwin", win32: "windows" }[process.platform]
const goarch = { x64: "amd64", arm64: "arm64" }[process.arch]

function buildFromSource() {
  const build = spawnSync("go", ["build", "-o", bin, path.join(root, "cmd", "relay-flow")], {
    cwd: root,
    stdio: "inherit",
  })
  if (build.status !== 0) process.exit(build.status ?? 1)
}

if (!fs.existsSync(bin)) {
  fs.mkdirSync(binDir, { recursive: true })
  let got = false
  if (goos && goarch && !isWin) {
    const asset = `relay-flow_${goos}_${goarch}.tar.gz`
    const url = `https://github.com/rajpopat27/relay-flow/releases/download/v${version}/${asset}`
    const archive = path.join(binDir, asset)
    const dl = spawnSync("curl", ["-fsSL", "-o", archive, url], { stdio: "inherit" })
    if (dl.status === 0) {
      const untar = spawnSync("tar", ["-xzf", archive, "-C", binDir], { stdio: "inherit" })
      fs.rmSync(archive, { force: true })
      if (untar.status === 0 && fs.existsSync(bin)) {
        fs.chmodSync(bin, 0o755)
        got = true
      }
    }
  }
  if (!got) buildFromSource()
}

const run = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" })
process.exit(run.status ?? 0)
