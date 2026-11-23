# Dependency Addition Policy

This document outlines the process for adding new dependencies to the Game Server Controller project.

## Overview

The project uses multiple dependency ecosystems:
- Go modules (primary implementation)
- Rust crates (optional conformance testing tool)
- Java/Gradle (Velocity bridge plugin)

## Adding a New Dependency

### 1. Assess Necessity

Before adding a dependency:
- Is there no existing solution or Go standard library alternative?
- Has the upstream maintained the package for at least 6 months?
- Does it actively maintain its issue tracker and release cycle?

### 2. License Compatibility Check

Consult [docs/LICENSING.md](LICENSING.md) for approved licenses. Common issues:

| License Type | Compatible? | Notes |
|--------------|--------------|-------|
| MIT, BSD, Apache 2.0, ISC | ✅ Yes | Preferred choices |
| Unlicense, Public Domain | ✅ Yes | No restrictions |
| MPL-2.0 | ✅ Yes | File-level copyleft; AGPL compatible |
| LGPL-2.1/3.0 | ⚠️ Review | Check linking requirements |
| GPL-2-only | ❌ No | Conflicts with AGPL |
| GPL-3.0-only | ⚠️ Review | May require dynamic linking or separate distribution |
| SSPL, CPL, etc. | ❌ No | Non-compatible copyleft |
| Proprietary | ❌ No | Not allowed in open-source project |

### 3. Add to go.mod (for Go dependencies)

**Example:**

```go
// Edit go.mod
require (
    // existing dependencies...
    github.com/new/package v1.2.3  // Add here
)
```

Then run:
```bash
go mod tidy
```

### 4. Create License Files

Create files in `licenses/`:

- `licenses/<package>-LICENSE`: Full license text from upstream
- `licenses/<package>-NOTICE`: Any copyright or NOTICE file content (if required)

**How to get license text:**
1. Go to the package's GitHub/Bitbucket/SRC website
2. Check root directory for LICENSE, NOTICE, or other license files
3. Copy full text exactly as provided
4. Save with appropriate filename pattern

### 5. Update THIRD_PARTY_NOTICES.md

Add a section following existing format:

```markdown
## github.com/new/package

- Module: `github.com/new/package`
- Version: `v1.2.3`, pinned in [go.mod](go.mod)
- Upstream: [url/to/repo]
- License: Apache-2.0 (or actual license)
- Copyright: See LICENSE file for notices
- Full license: [licenses/new-package-LICENSE](licenses/new-package-LICENSE)
```

### 6. Add to Third-Party Notices Index

Update the top-level header of THIRD_PARTY_NOTICES.md if needed to reflect new dependencies.

### 7. Test and Verify

Run pre-push checks:
```bash
go mod verify
go test ./...
gofmt -l .
go vet ./...
go run ./cmd/protocol-check
go run ./cmd/config-check validate configs/controller.example.yaml
```

## Special Cases

### Test-Only Dependencies

Dependencies used only in tests (only appear in go.sum, not go.mod):
- **Not** included in THIRD_PARTY_NOTICES.md unless vendored
- Document them in docs/DEPENDENCY_POLICY.md test section if frequently changed

### Transitive Dependencies

Dependencies of dependencies (indirect requires):
- Included if they modify shipped binaries or are needed for production builds
- Omitted if purely dev/test and not vendored

### Rust Crates (conformance-rs)

Crates in the optional `conformance-rs` tool:
- Documented in conformance-rs/Cargo.lock table
- Not redistributed by the main project binary
- Use separate license audit from Go ecosystem

### Gradle/Java Dependencies

For Velocity plugin compile-only dependencies:
- Document licenses per velocity-plugin README
- Verify against Maven Central published POM files
- Plugin JAR does not bundle these libraries; runtime comes from Velocity proxy

## Auditing Existing Dependencies

### Regular Audit Schedule

- **Monthly**: Quick scan of go.mod, Cargo.lock, build.gradle.kts for changes
- **Quarterly**: Deep audit including transitive dependencies via go mod graph
- **Annually**: Full re-verification of all upstream licenses and notices

### Before Upgrading Dependencies

1. Create a feature branch (never main)
2. Upgrade dependency to desired version
3. Run CI checks locally first
4. Verify compatibility with existing code
5. Check for license changes in the new release
6. If license file is missing or unclear, fetch from upstream and document

## Emergency Actions

### Dependency Shows Malicious Behavior

If a dependency:
- Contains malware/backdoors
- Changes license incompatibly
- Abandons maintenance unexpectedly

**Actions:**
1. Document evidence and report to project maintainers immediately
2. Evaluate migration path to alternative
3. Update THIRD_PARTY_NOTICES.md with security notice if needed
4. Consider using vendoring to lock version permanently
5. For critical issues, may require emergency release process

## Review Process

All dependency additions should be:
- [x] Documented in this policy section first
- [x] Added to go.mod (or equivalent) via Pull Request
- [x] LICENSE and NOTICE files created in licenses/
- [x] THIRD_PARTY_NOTICES.md updated with full entry
- [x] All CI checks pass locally before push
- [x] Pre-push verification checklist run (gofmt, go vet, tests)

## Resources

- [AGPL-3.0 License](https://www.gnu.org/licenses/agpl-3.0.html)
- [License Compatibility Database](https://tldrlegal.com/)
- [OSI Approved Licenses](https://opensource.org/licenses/alphabetical)
- [SPDX License List](https://spdx.org/licenses/)

## Last Updated

November 2026
