# Security Hardening Review

## Scope

This branch contains follow-up security and release-validation hardening after `v0.5.26`.
It is based on the exact `v0.5.26`/`main` commit
`f673e71862ca9a0c70853e85803e9663a1a8bcc3` and is intended to be reviewed and merged into
`main` before the next version is tagged.

No public SDK API is intentionally added, changed, or removed by this branch.

## Priority definitions

- **P0 — Must fix:** Required for a security-clean or reliably validated release.
- **P1 — Recommended:** Meaningful defense-in-depth or process improvement that is not an
  independent release blocker.
- **P2 — Optional:** Safe to defer without changing the security posture of the approved SDK code.

## Proposed changes

| ID | Change | Reason | Risk if omitted | Priority | Must fix? |
|---|---|---|---|---|---|
| SH-1 | Require Go 1.25.14 instead of an unspecified Go 1.25 patch release | Go 1.25 remains intentional, while the patch-level requirement prevents builds with earlier Go 1.25 standard libraries that contain known vulnerabilities. | Consumers may compile the SDK with an older vulnerable toolchain. The higher minimum patch version is also a compatibility decision and requires review. | P0 | **Yes**, for a security-clean release |
| SH-2 | Update `github.com/go-chi/chi/v5` from 5.0.7 to 5.3.0 and `golang.org/x/net` from 0.4.0 to 0.56.0, including checksums and vendor content | The versions on `v0.5.26` produced dependency advisories. The proposed versions pass `govulncheck` with Go 1.25.14. | Known dependency advisories remain in the module graph. | P0 | **Yes**, for a security-clean release |
| SH-3 | Restrict diagnostics configuration-file permissions before persisting credentials and propagate write, sync, and close failures | The example stores application API keys and tenant tokens. `os.WriteFile(..., 0600)` does not tighten the permissions of an existing file. | An existing broadly readable Unix file can remain broadly readable after secrets are written. | P0 | **Yes** |
| SH-4 | Warn when `-insecure` disables TLS certificate verification and document its diagnostic-only use | Makes an explicitly unsafe troubleshooting mode visible at runtime and discourages production use. TLS verification remains enabled by default. | Operators can select the unsafe option without a prominent runtime reminder. | P1 | No |
| SH-5 | Add regression tests for new and existing credential configuration files | Verifies secret round-tripping and Unix owner-only permissions, protecting SH-3 from regression. | Credential-file hardening would lack focused automated coverage. | P1 | No independently; include with SH-3 |
| SH-6 | Accept EOF and WebSocket `GoingAway` as normal test-server shutdown outcomes | Repeated validation exposed an intermittent `Test_E2E` false failure after delivery and cleanup had completed. The test passed 100 consecutive runs after this test-only correction. | CI can fail intermittently even when SDK behavior is correct, undermining release confidence. Production behavior is unchanged. | P0 | **Yes**, for reliable validation |
| SH-7 | Run the unit-test workflow on `v*` tag pushes | Static analysis already runs on tags. This adds a race-enabled unit-test record for tag builds as defense-in-depth. | A tag push does not independently start unit tests, although pull-request and `main` CI remain the primary pre-tag gates. | P1 | No |

## Files and ownership

- `.github/workflows/test.yaml` — SH-7
- `go.mod`, `go.sum` — SH-1 and SH-2
- `vendor/github.com/go-chi/chi/v5/**`, `vendor/golang.org/x/net/**`, and
  `vendor/modules.txt` — generated consequences of SH-2
- `examples/read-stream-diagnostics/main.go` — SH-3 and SH-4
- `examples/read-stream-diagnostics/main_test.go` — SH-5
- `examples/read-stream-diagnostics/README.md` — SH-3 and SH-4
- `internal/pubsub/test/server.go` — SH-6

## Security review notes

- No real credentials, private keys, or certificates are added. Test values are nonfunctional
  fixtures.
- TLS certificate verification remains enabled by default. The existing `-insecure` option is
  explicitly limited to controlled diagnostic environments and now emits a warning.
- Diagnostic logging continues to exclude message payloads and credentials.
- Dependency versions are pinned in `go.mod`/`go.sum`, and vendored content is regenerated from
  those versions.
- The configuration file is restricted before new credential content is written, avoiding a window
  in which newly persisted secrets inherit an existing permissive Unix mode.

## Required validation before merge

- Full tests with Go 1.25.14.
- Repeated `internal/pubsub` and `Test_E2E` runs to verify the shutdown flake is resolved.
- Linux race-enabled tests in pull-request CI.
- `go vet` and `golangci-lint`.
- `govulncheck` with Go 1.25.14.
- Module checksum and vendor consistency checks.
- Formatting, diff-hygiene, credential-pattern, TLS, and public-API reviews.

## Local validation completed

The following checks passed on Windows using the official Go 1.25.14 toolchain:

- uncached full test suite;
- `Test_E2E` 100 consecutive times;
- the complete `internal/pubsub` package 20 consecutive times and, in a separate longer soak,
  50 consecutive times;
- `go vet` and `golangci-lint`;
- `govulncheck` v1.7.0, which scanned 15 modules and Go 1.25.14 and reported no
  vulnerabilities;
- `go mod verify` and vendored package resolution;
- Go formatting and Git diff hygiene; and
- credential-pattern, TLS-bypass, weak-cryptography, and public-API scope reviews.

Race testing remains a required pull-request CI gate because the local Windows Go environment has
CGO disabled and no C compiler.
