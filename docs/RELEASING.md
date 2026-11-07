# Release process

Releases must be reproducible from reviewed source and must not depend on AI,
production credentials, or live infrastructure.

## Before a controller release

1. Merge the implementation through a reviewed pull request. Do not release from
   a dirty worktree or an unreviewed branch.
2. Confirm the Phase 2 fake-adapter conformance gate has passed. Until then,
   publish no stable `1.0` protocol or controller release.
3. Run the checks listed in [Contributing](../CONTRIBUTING.md), including the
   protocol validator and fake-adapter tests. CI results must match the exact
   commit being released.
4. Review dependency changes in `go.mod` and `go.sum`. Every direct dependency
   needs a documented purpose and license; `go mod verify` checks the cached
   module contents against their recorded checksums.
5. Record the source commit and Go toolchain version with each binary. Publish
   checksums for release artifacts and retain the exact source revision used to
   build them.
6. Create an immutable version tag from the reviewed commit. Never move or
   replace a published tag. Correct a release by publishing a new version.

## Protocol draft releases

The current protocol is `0.21.0-draft`. Each published draft snapshot is
immutable. Increment the pre-1.0 minor version for the next snapshot, update
version-scoped schema IDs and examples together, and validate the complete
schema, example, OpenAPI, and conformance package before tagging it. Keep the
published package digest or equivalent manifest with the release so independent
implementations can reproduce the exact package.

Draft tags do not claim stable interoperability. Stable `1.0` requires the
reviewed Go controller to pass the published behavioral suite with scripted
fake adapters and no production credentials or live infrastructure. Other
language implementations may contribute by running the same suite, but they
are not a release prerequisite.
