import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

/**
 * Pi entry point for relay-flow-plugin.
 *
 * Pi loads extensions for ordinary sessions too. Relay-flow behavior is only
 * enabled for sessions launched by the harness, identified by the pair of
 * relay-flow identity variables. Lifecycle handlers are added by the later
 * Pi integration tasks; until then, this factory intentionally does no work.
 */
export default function relayFlowPi(_pi: ExtensionAPI): void {
  if (!process.env.RELAY_FLOW_RUN_ID && !process.env.RELAY_FLOW_NODE) return;
}
