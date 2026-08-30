import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const temporaryDirectories: string[] = [];

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

describe("published plugin", () => {
  test("packs and loads its runtime entry", async () => {
    const directory = mkdtempSync(join(tmpdir(), "relay-flow-plugin-"));
    temporaryDirectories.push(directory);
    const archiveName = "relay-flow-plugin.tgz";
    const archive = join(import.meta.dir, archiveName);

    const pack = Bun.spawnSync([
      "bun", "pm", "pack",
      "--filename", archiveName,
      "--ignore-scripts",
      "--quiet",
    ], { cwd: import.meta.dir });
    expect(pack.exitCode).toBe(0);
    try {
      const extract = Bun.spawnSync(["tar", "-xzf", archive, "-C", directory]);
      expect(extract.exitCode).toBe(0);
    } finally {
      rmSync(archive, { force: true });
    }

    const packaged = await import(join(directory, "package", "relay-flow.ts"));
    expect(typeof packaged.default).toBe("function");
  });
});
