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
- **Fixtures:** its own evaluator for each fixture below, compared with the fixture's published `expected` object.

| Fixture | Status |
|---|---|
| `protocol.json.raw-wire-interoperability` | evaluated |
| `operations.type-binding` | evaluated |
| `operations.restore.final-state-policy` | evaluated |
| `operations.backup.no-resume-final-state` | evaluated |
| `operations.backup.initial-state-resume-policy` | evaluated |
| `integration.network-profile.requirements` | evaluated |
| The other 11 fixtures of the initial interoperability profile | not yet; listed in the report under `profile_fixtures_not_covered` |

Until every profile fixture is evaluated, the report claims `exploratory-subset-of-initial-profile`, not the full profile.

## Limitations

Offline message vectors only. They do not prove that an adapter avoids side effects; that needs an implementation harness that records attempted effects.

## Third-party crates

Every crate in `Cargo.lock` is credited in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md). `cargo test` fails if one is missing.
