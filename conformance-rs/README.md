# conformance-rs

An independent Rust consumer of this repository's protocol package. It reads the published schemas, examples, and conformance fixtures directly, never imports or runs the Go code, and writes an evidence report in the shape described in [the interoperability plan](../docs/PROTOCOL_INTEROPERABILITY_PLAN.md#evidence-to-retain). It is optional and not a release gate; repository validation and release tooling remain Go.

## Run it

```bash
cd conformance-rs
cargo run --locked -- --package .. --report report.json
```

The command exits non-zero if any fixture it evaluates fails, or if the recomputed package digests do not match `protocol/conformance/package-manifest.json`. CI runs it in `.github/workflows/conformance-rs.yml` and prints the report.

## What it checks

- **Package identity:** recomputes `sha256-path-filehash-v1` for the schema registry and the fixture suite from the algorithm documented in `protocol/conformance/requirements.md`.
- **JSON rules:** a parser written here, which every package file goes through first. It rejects invalid UTF-8, duplicate member names (compared after unescaping), unpaired escaped surrogates, non-finite numbers, and integral numbers outside ±(2^53 − 1).
- **Schemas:** compiles every schema in `protocol/schemas/index.json` with the `jsonschema` crate (draft 2020-12, `format` asserted, offline), and validates every example listed in `protocol/examples/index.json`.
- **Canonical JSON:** RFC 8785 JCS, written from the RFC (UTF-16 member ordering, minimal string escapes), with ECMAScript number formatting from the `ryu-js` crate. It is used for the `game.configure` digests and tested against the RFC's sample values.
- **Fixtures:** its own evaluator for each fixture below, written from the normative protocol text and compared with the fixture's published `expected` object.

It evaluates every fixture of the initial interoperability profile, plus the network-profile requirements:

| Area | Fixtures |
|---|---|
| JSON wire | `protocol.json.raw-wire-interoperability` |
| Operations | `operations.type-binding`, `operations.restore.final-state-policy`, `operations.backup.no-resume-final-state`, `operations.backup.initial-state-resume-policy` |
| Game | `game.release.describe.integrity-and-identity`, `game.install.release-binding-and-reconciliation`, `game.configure.pinned-schema-and-result-binding` |
| Execution | `execution.unit.start.accepted-not-ready`, `execution.unit.graceful-stop.unknown-requires-inspection`, `execution.unit.inspect.exact-target-state`, `execution.unit.logs.byte-bound-and-redaction`, `execution.unit.health.diagnostic-redaction-and-binding` |
| Proxy | `integration.proxy.register.accepted-is-not-routable`, `integration.proxy.players.inspect.fresh-count-and-target-binding` |
| Velocity | `velocity-transfer-outcome-unknown-reconciliation`, `velocity-transfer-privacy-canaries` |
| Network | `integration.network-profile.requirements` (not part of the profile) |

When all of them pass, the report claims `initial-interoperability-profile`. If a future package adds a profile fixture this consumer cannot evaluate, the claim drops back to `exploratory-subset-of-initial-profile` and the report lists the missing fixture under `profile_fixtures_not_covered`.

## Limitations

Offline message vectors only. They do not prove that an adapter avoids side effects; that needs an implementation harness that records attempted effects.

## Third-party crates

Every crate in `Cargo.lock` is credited in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md). `cargo test` fails if one is missing.
