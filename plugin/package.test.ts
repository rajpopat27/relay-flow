import { afterEach, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const temporaryDirectories: string[] = [];

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

describe("published plugin", () => {
  test("packs and loads its runtime entries", async () => {
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

    const packagedServer = await import(join(directory, "package", "relay-flow.ts"));
    const packagedTui = await import(join(directory, "package", "tui.ts"));
    const piEntry = await import(join(directory, "package", "pi.ts"));
    expect(typeof packagedServer.default).toBe("function");
    expect(packagedTui.default.id).toBe("relay-flow-plugin.tui");
    expect(typeof packagedTui.default.tui).toBe("function");
    expect(typeof piEntry.default).toBe("function");

    const manifest = JSON.parse(readFileSync(join(directory, "package", "package.json"), "utf8"));
    expect(manifest.main).toBe("./relay-flow.ts");
    expect(manifest.pi.extensions).toEqual(["./pi.ts"]);
    expect(manifest.files).toEqual([
      "relay-flow.ts",
      "tui.ts",
      "pi.ts",
      "transport.ts",
      "index.ts",
      "README.md",
    ]);

    const piSource = readFileSync(join(directory, "package", "pi.ts"), "utf8");
    const openCodeSource = readFileSync(join(directory, "package", "relay-flow.ts"), "utf8");
    const readme = readFileSync(join(directory, "package", "README.md"), "utf8");
    expect(piSource).toContain('from "./transport"');
    expect(piSource).toContain('from "./index"');
    expect(piSource).toContain('ctx.ui.select');
    expect(openCodeSource).toContain('from "./transport"');
    expect(readme).toContain("pi install npm:relay-flow-plugin@");
    expect(readme).toContain("pi.extensions");
    expect(readme).toContain("interactive TUI");
    expect(readme).toContain("ctx.ui.select()");
  });
});
