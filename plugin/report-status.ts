// Minimal opencode plugin: on session.idle, forward the agent's last
// message text to `orca-jira-loop report` as a one-shot CLI call. That
// CLI only talks to Jira (via acli) and decides what to do — it never
// talks to Orca or opencode. This plugin never closes its own terminal;
// terminals are left open regardless of outcome and cleaned up
// separately. No network transport, no persisted state — ticket/agent
// come from env vars the daemon set on this terminal.
// See docs/jira-workflow-architecture.md section 11.

import { execFile } from "node:child_process"
import { appendFileSync, mkdirSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"
import type { Plugin } from "@opencode-ai/plugin"

// Plugin logs go to a file, never console: console output surfaces in the
// opencode UI message stream, which is noise for the user.
const logDir = join(homedir(), ".orca-jira-loop")
const logFile = join(logDir, "plugin.log")
function log(...args: unknown[]) {
  try {
    mkdirSync(logDir, { recursive: true })
    const line = `[${new Date().toISOString()}] ${args.map((a) => (typeof a === "string" ? a : JSON.stringify(a))).join(" ")}\n`
    appendFileSync(logFile, line)
  } catch {
    // never let logging break the plugin
  }
}

function execFileText(cmd: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { encoding: "utf8" }, (err, stdout) => {
      if (err) return reject(err)
      resolve(stdout)
    })
  })
}

// Deterministic parser for the agent's final STATUS/SUMMARY block. The
// daemon gets the parsed values from this function, never the raw LLM
// output, so the Go side needs no regex at all.
export function parseStatusBlock(output: string): {
  status: string
  summary: string
  valid: boolean
} {
  const normalized = output.replace(/^[ \t]*[*_#`•\-]*/gm, "")
  const statusMatch = /^\s*STATUS:\s*\**\s*(?!\s*SUMMARY:)(.+?)(?=\s+\W*SUMMARY:|\s*$)/im.exec(normalized)
  const summaryMatch = /(?:^|\s|[*_#`•\-])SUMMARY:\s*\**\s*(.+)$/im.exec(normalized)
  const status = statusMatch?.[1]?.replace(/[*_#`]/g, "").trim().replace(/[.,;!]+$/, "") ?? ""
  const summary = summaryMatch?.[1]?.replace(/^[*_#`]+|[*_#`]+$/g, "").trim() ?? ""
  const summaryWords = summary.split(/\s+/).filter(Boolean)
  return { status, summary, valid: Boolean(status) && summaryWords.length >= 10 }
}

export const ReportStatusPlugin: Plugin = async ({ client }) => {
  const configName = process.env.ORCA_JIRA_LOOP_CONFIG
  const workflowName = process.env.ORCA_JIRA_LOOP_WORKFLOW
  const ticket = process.env.ORCA_JIRA_LOOP_TICKET
  const agent = process.env.ORCA_JIRA_LOOP_AGENT
  const expectedTitle = ticket && agent ? `${ticket}:${agent}` : undefined

  return {
    event: async ({ event }: { event: any }) => {
      // Set title on first idle (after opencode's naming agent has run),
      // then proceed to report.
      if (event?.type === "session.idle") {

        if (!configName || !workflowName) return

        const sessionID: string | undefined = event?.properties?.sessionID
        if (!ticket || !agent || !sessionID) {
          log("[report-status] ORCA_JIRA_LOOP_TICKET/_AGENT or sessionID missing, skipping report")
          return
        }

        // Pin the title now that the naming agent has done its thing.
        if (expectedTitle) {
          try {
            await client.session.update({
              path: { id: sessionID },
              body: { title: expectedTitle },
            })
          } catch (err) {
            log("[report-status] failed to set session title:", err)
          }
        }

        // session.idle only carries {sessionID} — the actual message text
        // must be fetched separately; there is no event.message field.
        const messages = await client.session.messages({ path: { id: sessionID } })
        // Find the last assistant message to check if it was aborted.
        const lastAssistant = messages.data?.filter((m: any) => m.info?.role === "assistant").pop()

        // If the last assistant message has no finish reason, the user
        // pressed Escape mid-generation — skip the report.
        const finish = (lastAssistant as any)?.info?.finish
        if (!finish) {
          log("[report-status] last assistant message has no finish reason (aborted), skipping report")
          return
        }

        // The last message may be our own nudge (user role). Find the last
        // assistant message with actual text output — that's the agent's
        // response we need to parse for STATUS/SUMMARY.
        const lastWithText = messages.data?.filter((m: any) =>
          m.info?.role === "assistant" &&
          (m.parts ?? []).some((p: any) => p.type === "text" && p.text?.trim())
        ).pop()

        const output = (lastWithText?.parts ?? [])
          .filter((p: any) => p.type === "text")
          .map((p: any) => p.text)
          .join("\n")

        // Cross-check against opencode's own ground truth for which agent
        // actually ran this turn (AssistantMessage.mode) — just a sanity
        // log, not the report identity (see comment on `agent` above).
        const realOpencodeAgent: string | undefined = (lastWithText as any)?.info?.mode
        if (realOpencodeAgent && realOpencodeAgent !== agent) {
          log(`[report-status] warning: env agent ${agent} differs from opencode's own mode ${realOpencodeAgent}`)
        }

        // Parse the agent's final STATUS/SUMMARY block deterministically right
        // here (the daemon receives the parsed values, not the raw text —
        // no regex re-parse on the Go side). Both may share one line
        // ("STATUS: done SUMMARY: …"). Weaker models wrap them in stray
        // markdown: strip leading markers per line and inline ** after
        // the labels, plus trailing * _ # ` and punctuation.
        const block = parseStatusBlock(output)
        if (!block.valid) {
          log("[report-status] no valid STATUS/SUMMARY block yet, nudging same session")
          await client.session.prompt({
            path: { id: sessionID },
            body: {
              parts: [{
                type: "text",
                text: "Your last message did not include a valid STATUS/SUMMARY block. Please end your turn with exactly:\nSTATUS: <status name>\nSUMMARY: <detailed summary>\nThe summary must be at least 10 words and cover what you changed, which files you touched, and how to verify it — a follow-up agent will work from it.",
              }],
            },
          })
          return
        }

        // "error" (Comment/Transition failed on the Go side) is just retried
        // by calling report again — no new LLM turn needed, the output text
        // is already known good. Bounded so a persistent Jira/acli outage
        // doesn't retry forever; falls through to the nudge/error path below.
        let action = "error"
        let detail = ""
        for (let attempt = 0; attempt < 3; attempt++) {
          try {
            const stdout = await execFileText("orca-jira-loop", [
              "report",
              "--config", configName,
              "--workflow", workflowName,
              "--ticket", ticket,
              "--agent", agent,
              "--status", block.status,
              "--summary", block.summary,
            ])
            const result = JSON.parse(stdout.trim()) as { action: string; detail: string }
            action = result.action
            detail = result.detail
            log("[report-status]", result.action, result.detail)
          } catch (err) {
            log(`[report-status] orca-jira-loop report failed (attempt ${attempt + 1}/3):`, err)
          }
          if (action !== "error") break
        }

        // Terminal-closing removed: it was implicated in terminals
        // disappearing before the daemon could observe a completed report.
        // Leave every terminal open regardless of action — a human (or a
        // future cleanup pass) closes it explicitly instead.
        if (action === "transitioned") {
          return
        }

        await client.session.prompt({
          path: { id: sessionID },
          body: {
            parts: [{
              type: "text",
              text: detail || "Your last message did not include a valid STATUS/SUMMARY block for this agent. Please try again.",
            }],
          },
        })
      }
    },
  }
}

export default ReportStatusPlugin
