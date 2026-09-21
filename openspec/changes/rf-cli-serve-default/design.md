# Design

## CLI mode resolution

`cmdServe` resolves the mode before starting a child or acquiring server
lifecycle state:

- no mode flag: detached startup;
- `--background`: detached startup for compatibility;
- `--foreground`: blocking startup;
- both mode flags: usage error (exit 2).

The detached parent launches the same executable with `serve` plus only the
`--recover` and `--debug` flags requested by the caller. An internal
environment marker selects the existing foreground serve path in that child;
user-facing mode flags are not recursively passed. The parent waits for the
Unix-socket API to respond and prints `Relay-flow server started` only after
readiness.

## Stable working directory

The launcher creates and validates `p.Root` before assigning it to the child
`exec.Cmd.Dir`. `RELAY_FLOW_HOME` is normalized to an absolute path. Real
foreground CLI processes anchor their process directory and `PWD` at `p.Root`
before the long-lived server starts, so inherited Orca CLI calls remain valid
when the original caller directory is deleted. Beads and other adapters retain
explicit ownership of their child command directories.

## Alias distribution

`cmd/rf` shares the production command sources with `cmd/relay-flow` and is
built as a second binary named `rf`; command parsing and behavior are not
forked. GoReleaser builds both binaries into each archive and installs/tests
both through the generated Homebrew formula. `install.sh` installs the
canonical binary and replaces an existing non-directory alias with a relative
`rf -> relay-flow` symlink. The npm wrapper keeps both `bin` entries pointed at
the same script.

## Verification

Go tests cover detached default startup, foreground startup, compatibility
startup, mode conflicts, readiness failures, child working directory, and
caller-directory deletion. Packaging tests cover npm metadata, Go installs,
release archives, generated Homebrew formula behavior, and the installer alias.
