# v4 Architecture — Tracker-Agnostic Workflow Engine

## ELI5

Imagine a board game where each ticket is a token moving across squares. The squares (nodes) are yours to name. Each square has a robot (agent) sitting on it. When a token lands on a square, the robot does its job, then says either "success" or "failure" — and the square's own rulebook (onSuccess/onFailure) says which square the token moves to next. Jira is just the scoreboard where the token's position is written down; swap Jira for beads and only the scoreboard-keeper changes, not the game.

## End-to-end walkthrough (one ticket, one workflow)

```mermaid
sequenceDiagram
    participant U as User
    participant S as Server
    participant P as Poll loop
    participant J as Jira adapter
    participant T as Terminal (OpenCode)
    participant PL as Plugin

    U->>S: submit workflow.yaml (name: xyzFlow)
    S->>S: validate v4, build jira tracker, start poll goroutine
    loop every 15s
        P->>J: List()
        J->>J: JQL search; map status→node via trackerMap
        J-->>P: [GHCOS-1 node=coding, ClaimedBy=""]
        P->>J: Claim(GHCOS-1) → label wf:xyzFlow
        P->>T: spawn worktree terminal<br/>title GHCOS-1:coder:coding<br/>env WORKFLOW/TICKET/NODE
        T->>T: agent codes, ends with STATUS/SUMMARY
        T->>PL: session.idle
        PL->>S: POST /report {xyzFlow, GHCOS-1, coding, success, summary}
        S->>J: Report(GHCOS-1, success → inReview)
        J->>J: transition GHCOS-1 → "In Review" + comment
        S-->>PL: {action: transitioned}
    end
    Note over P,J: next tick: GHCOS-1 now node=inReview<br/>→ reviewer agent spawns, same dance
    Note over P: server restart? resubmit → claimed tickets<br/>come back as bounce (nudge existing terminal)
```


## Goroutine map

```mermaid
flowchart TD
    Main["main() — 1 goroutine"] --> Serve["cmdServe → server.Serve()<br/>http.Server on unix socket<br/>(Go spawns 1 goroutine per connection)"]
    Serve --> SubmitH["/submit handler"]
    SubmitH -->|"per workflow in yaml"| Poll["go pollLoop(workflow)<br/>1 goroutine per workflow<br/>lives until server stop"]
    Poll -->|"every 15s tick"| Tickets{"per ticket in List()"}
    Tickets -->|"unclaimed"| Dispatch["go dispatch(ticket, node)<br/>1 short-lived goroutine:<br/>Claim → spawn Orca terminal → exit"]
    Dispatch -.->|"terminal + opencode session<br/>(external processes, not goroutines)"| OC["OpenCode agent"]
    OC --> PluginH["plugin → POST /report<br/>(server conn goroutine) →<br/>tracker.Report → transition"]
```

Goroutines: 1 per workflow poll loop (long-lived), 1 per ticket dispatch (short-lived), 1 per socket connection (Go stdlib). Terminals/agents are OS processes, not goroutines.

## Component view

```mermaid
flowchart LR
    subgraph Server["orca-wf server (unix socket, flock single-instance)"]
        Submit["POST /submit<br/>(yaml → config + tracker + poll goroutine)"]
        Report["POST /report<br/>{workflow, ticket, node, outcome, summary}"]
        Shutdown["POST /shutdown"]
        Daemon["Daemon<br/>per-workflow poll loop<br/>in-memory claimed set"]
    end

    subgraph Adapters["tracker.Tracker (registry, adapters self-register)"]
        Jira["jira adapter<br/>JQL + trackerMap<br/>claim label wf:&lt;name&gt;"]
        Beads["beads adapter<br/>(user-supplied)"]
        GH["github adapter<br/>(user-supplied)"]
    end

    subgraph Orca["Orca + OpenCode"]
        Term["terminal per ticket<br/>title: ticket:agent:node<br/>env: WORKFLOW/TICKET/NODE"]
        Plugin["report plugin<br/>parses STATUS/SUMMARY<br/>calls orca-wf report"]
    end

    Submit --> Daemon
    Daemon -->|"List() → tickets w/ Node, ClaimedBy"| Jira
    Daemon -->|"Claim() → label"| Jira
    Daemon -->|spawn + env| Term
    Term --> Plugin
    Plugin -->|"orca-wf report (socket)"| Report
    Report -->|"tracker.Report(outcome, targetNode)"| Jira
    Jira -->|"transition / comment<br/>(self-loop = comment only)"| JiraCloud[(Jira)]
```

## Poll cycle (per workflow)

```mermaid
flowchart TD
    A["poll tick"] --> B["tracker.List()<br/>all tickets, Node + ClaimedBy filled"]
    B --> C{per ticket}
    C -->|"ClaimedBy = other workflow"| D[skip]
    C -->|"node unmapped + terminal"| E[close terminals]
    C -->|"ClaimedBy = me, not in memory<br/>(post-restart)"| F["bounce:<br/>reuse terminal, nudge session"]
    C -->|"unclaimed"| G["Claim() → spawn terminal<br/>env: ORCA_WF_*"]
    G --> H["agent works → STATUS/SUMMARY"]
    H --> I["plugin → POST /report"]
    I --> J["tracker.Report:<br/>outcome → onSuccess/onFailure → targetNode<br/>transition tracker state + comment"]
```

## Example node graph (yaml)

```mermaid
stateDiagram-v2
    [*] --> coding
    coding --> inReview: success
    coding --> coding: failure
    inReview --> done: success
    inReview --> coding: failure
    done --> [*]
```

```yaml
nodes:
  coding:   {agent: coder,    onSuccess: inReview, onFailure: coding}
  inReview: {agent: reviewer, onSuccess: done,     onFailure: coding}
  done: {}                       # terminal
trackerMap:                      # jira adapter interprets
  coding: "In Progress"
  inReview: "In Review"
  done: "Done"
```
