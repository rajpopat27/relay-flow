const assert = require("node:assert/strict")
const test = require("node:test")
const packageJSON = require("./package.json")

test("npm package exposes relay-flow and rf aliases", () => {
  assert.equal(packageJSON.bin["relay-flow"], "./bin/relay-flow.js")
  assert.equal(packageJSON.bin.rf, "./bin/relay-flow.js")
})
