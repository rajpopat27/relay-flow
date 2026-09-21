## 1. CLI behavior

- [x] 1.1 Make plain `serve` detached and readiness-gated.
- [x] 1.2 Add blocking `--foreground` and preserve `--background`.
- [x] 1.3 Reject conflicting mode flags before startup.
- [x] 1.4 Propagate `--recover` and `--debug` once without recursive mode flags.

## 2. Stable process directory

- [x] 2.1 Normalize configured home paths to absolute paths.
- [x] 2.2 Anchor detached children at the durable relay-flow root.
- [x] 2.3 Anchor real foreground processes and cover deleted caller directories.

## 3. Alias and packaging coverage

- [x] 3.1 Add the shared `cmd/rf` developer entrypoint.
- [x] 3.2 Package/test both binaries with GoReleaser and Homebrew.
- [x] 3.3 Install an idempotent `rf` alias from `install.sh`.
- [x] 3.4 Preserve npm alias metadata and update E2E setup to use real `rf`.

## 4. Documentation and verification

- [x] 4.1 Update active CLI, onboarding, README, E2E, and workflow/repo specs.
- [x] 4.2 Add focused CLI and packaging regression coverage.
- [x] 4.3 Run the Go, race, vet, JavaScript, OpenSpec, release, and shell checks.
