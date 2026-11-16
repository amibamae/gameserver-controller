# Game Server Controller Licensing Policy

This document describes the licensing of the Game Server Controller project and its third-party dependencies.

## Project License

The Game Server Controller is released under the **GNU Affero General Public License v3.0 or later (AGPL-3.0-only)**. See [LICENSE](../LICENSE) for the full license text.

### AGPL-3.0 Requirements

As an AGPL-3.0 project:

1. **Source Code Availability**: All source code modifications must be made available under the same license when distributing the software.
2. **Copyleft Propagation**: Derivative works must also be licensed under AGPL-3.0.
3. **Affero Clause**: Software running over a network (network services) must make source code available to all users.
4. **Third-Party Licenses**: All third-party dependencies are separately licensed and do not replace the AGPL-3.0 license.

## Third-Party Dependency Policy

### Adding Dependencies

When adding new third-party dependencies:

1. **License Compatibility**
   - Prefer licenses compatible with AGPL-3.0 (permissive or weak copyleft)
   - Avoid strong copyleft licenses unless necessary (GPL-2/3, LGPL)
   - Check license compatibility with AGPL-3.0 requirements
   
2. **License Documentation**
   - Add full license text to `licenses/<package>-LICENSE`
   - Include copyright notices and upstream attribution
   - For packages with NOTICE files, include them in `licenses/<package>-NOTICE`

3. **Documentation Update**
   - Document the dependency in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)
   - Include version, license, copyright, and source information

4. **Approved Licenses**
   The following licenses are generally considered compatible with AGPL-3.0:
   - MIT License
   - BSD licenses (2-Clause, 3-Clause)
   - Apache License 2.0
   - ISC License
   - Unlicense / Public Domain
   - MPL-2.0 (Mozilla Public License)
   - LGPL-2.1 or LGPL-3.0 (with proper dynamic linking where applicable)

### Reporting Incompatible Licenses

If a dependency has:
- Proprietary/Commercial license
- GPL-2-only (without AGPL option)
- SSPL
- Or any other license that may conflict with AGPL-3.0

You must consult the maintainers before adding it. Consider alternatives if available.

## Dependency Ecosystems

### Go Modules

Direct and indirect dependencies from `go.mod` are included in THIRD_PARTY_NOTICES.md. Test-only dependencies (only in go.sum) are excluded unless vendored.

### Rust Crates (conformance-rs)

The optional Rust consumer `conformance-rs` has its own Cargo.lock. Dependencies are documented there and not redistributed by the main project.

### Java/Gradle (Velocity Plugin)

The Velocity bridge plugin declares compile-only dependencies; runtime libraries come from the Velocity proxy. All licenses are documented in THIRD_PARTY_NOTICES.md under the Velocity bridge section.

## License Files Location

- `licenses/`: Contains LICENSE and NOTICE files for third-party dependencies
- Each file follows the pattern: `<package>-LICENSE` or `<package>-NOTICE`

## Verification

### Pre-Push Checklist

Before committing changes that add/remove dependencies:

```bash
# Verify Go formatting
gofmt -l .

# Static analysis
go vet ./...

# Run tests (without pushing to production)
go test ./...

# Protocol conformance check
go run ./cmd/protocol-check

# Validate config files
go run ./cmd/config-check validate configs/controller.example.yaml
```

### License Compliance Checks

CI workflows include:
- Dependency version pinning verification
- License file presence checks
- NOTICE file inclusion validation

## CI Integration

GitHub Actions workflows ([.github/workflows/*.yml](.github/workflows/)) include:
- `go-validation.yml`: Go build and test verification
- `protocol-validation.yml`: Protocol conformance checking
- `conformance-rs.yml`: Optional Rust conformance tests (MIT licensed tooling)

## Auditing Dependencies

To audit dependencies:

### Go
```bash
# List all dependencies with versions
go list -m all

# Check for transitive dependency license issues
go mod graph
```

### Rust
```bash
# In conformance-rs directory
cargo tree --edges normal
```

### Java/Gradle
```kotlin
// For Velocity plugin, run with Gradle:
./gradlew dependencies --configuration compileClasspath
```

## Contact and Escalation

If you encounter:
- Unclear license terms in a dependency
- License compatibility questions
- Upstream license change notifications

Please document the issue and escalate to the project maintainers.

## Last Updated

November 2026
