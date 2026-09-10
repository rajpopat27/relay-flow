## MODIFIED Requirements

### Requirement: Command surface is grouped by resource

The supported command surface SHALL include:

```text
relay-flow init [--force]
relay-flow serve [--recover] [--debug] [--background]
relay-flow stop
relay-flow report
relay-flow version
relay-flow --version
relay-flow -v

relay-flow workflow submit --file <path>
relay-flow workflow remove --name <name>
relay-flow workflow list
relay-flow workflow get --name <name>

relay-flow repo register
relay-flow repo remove --name <name>
relay-flow repo list
relay-flow repo get --name <name>

relay-flow run list
relay-flow run get --ticket <key>
relay-flow run cancel --ticket <key>
```

Command groups and leaf commands SHALL support `--help` and `-h`. Help output SHALL be scoped to the requested command: the root command lists grouped top-level commands, a resource command lists only its direct subcommands, and a leaf command lists only its own options and examples. Help SHALL not require the server or initialized durable state. There SHALL be no separate workflow update command; submit performs create-or-replace subject to active-run restrictions.

#### Scenario: Existing workflow is submitted

- **WHEN** the workflow name exists and has no active runs
- **THEN** submit replaces it without requiring an update command

#### Scenario: Stop command is used

- **WHEN** `relay-flow stop` is invoked
- **THEN** the server stops accepting new work, performs bounded shutdown, and exits

#### Scenario: Version command is used

- **WHEN** `relay-flow version`, `relay-flow --version`, or `relay-flow -v` is invoked
- **THEN** the CLI prints version information without contacting the server and exits zero

#### Scenario: Resource-scoped help is used

- **WHEN** `relay-flow workflow --help` is invoked
- **THEN** only workflow commands and workflow-level guidance are printed

#### Scenario: Leaf-scoped help is used

- **WHEN** `relay-flow run get --help` is invoked
- **THEN** only run-detail options and examples are printed and no server request is made
