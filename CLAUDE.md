# Instructions for AI assistants working in this repository

## Third-party software: disclose and credit

The project owner requires this for all work, including future conversations.

- Every third-party dependency, build tool, CI action, and runtime-provided library that the project uses or ships must be credited in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Cover Go modules, Rust crates, Java/Gradle libraries and tooling, JDKs, GitHub Actions, and anything the Velocity plugin links against at runtime.
- Each entry gives the name, version, license, and attribution (copyright holder or maintainer), and links the upstream notice. Put the full license text in `licenses/` when the license requires it.
- Check the license at its source (upstream repository, package metadata, or the jar/POM). If you cannot confirm a license, say so in the entry and in your PR; do not guess.
- Update THIRD_PARTY_NOTICES.md in the same pull request that adds, upgrades, or removes the software. Do not add a dependency without a notice entry.

## Project rules

- Follow and update the protocol for every change: schemas, examples, conformance vectors, and the manifest (`go run ./cmd/protocol-check --manifest > protocol/conformance/package-manifest.json`).
- Run `gofmt -l .`, `go vet ./...`, `go test ./...`, and `go run ./cmd/protocol-check` before pushing.
- Open a pull request for each change, merge after CI is green, and delete the branch.
- Integration is a responsibility category, not an optional one. Every backend gets its own domain from one naming scheme.
