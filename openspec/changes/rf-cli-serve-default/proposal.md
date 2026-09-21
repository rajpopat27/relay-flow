# Make `rf` a first-class alias and detach `serve` by default

## Why

Relay-flow currently has an inconsistent shorthand installation story: npm
exposes `rf`, while developer installs, release archives, Homebrew, and
`install.sh` do not. In addition, background startup inherits the caller's
working directory. A server launched from an Orca worktree can therefore keep
running with a deleted current directory and fail later when the runner invokes
an external CLI.

## What changes

- Expose one CLI implementation as both `relay-flow` and `rf` through npm,
  Go developer installs, release archives, Homebrew, and `install.sh`.
- Make plain `serve` detached and readiness-gated.
- Add blocking `serve --foreground` for supervisors and development.
- Keep `serve --background` as a compatibility spelling for detached startup.
- Anchor detached children and real foreground server processes at the durable
  relay-flow state root instead of the caller's temporary directory.
- Document the new mode/alias contract and update installation/E2E smoke
  coverage.

## Non-goals

- Do not change the relay-flow state layout, Unix-socket API, workflow engine,
  task-system routing, report contract, or runner lifecycle.
- Do not remove `relay-flow` or `--background`.
- Do not add adapter-level `os.Chdir`; task and runner adapters retain their
  own child-process directory responsibilities.
- Do not manage the Orca daemon or repair processes that already have a
  deleted working directory.
