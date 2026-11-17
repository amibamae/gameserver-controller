# Development Workflow

This guide explains how to develop, test, and contribute to Game Server Controller.

---

## Branching Strategy

- **main**: Stable release branch; never push directly here
- **feature/***: New features or significant changes
- **bugfix/***: Bug fixes for released versions

Each change gets its own branch with clear commit messages describing the fix or feature.

### Before Pushing

Run these checks locally:

```bash
# Code formatting - should print nothing
gofmt -l .

# Static analysis
go vet ./...

# Protocol conformance
go run ./cmd/protocol-check

# Configuration validation
go run ./cmd/config-check validate configs/controller.example.yaml
```

For your changed functions specifically, also run:

```bash
go run ./cmd/complexity-check -base origin/main
```

This must report nothing for any function you changed. See [CLAUDE.md](../CLAUDE.md) for the complexity exception rules.

---

## Running Tests

### Basic Tests

```bash
go test -count=1 ./...
```

### Race Detection

```bash
go test -race -count=1 ./...
```

Tests must pass before committing. Never point tests at production systems or real credentials. Use fake adapters in `internal/fakeadapter/` for testing adapter logic.

---

## Commit Messages

Follow conventional commits:

```
feat: add new feature description
fix: fix bug with detailed explanation
docs: update documentation
refactor: code restructuring without behavior change
test: add or update tests
chore: build system or dependency changes
```

Each commit should be atomic—one logical change per commit.

---

## Pull Request Process

1. Create feature branch from main
2. Implement changes with tests
3. Run pre-push checks locally
4. Submit PR to maintainer review
5. Address review feedback
6. CI must pass (green before merge)
7. Maintainer merges after approval

### What to Include in PR

- What you changed and why
- Components affected (compatibility changes, side effects)
- Tests added or modified
- Documentation updates
- New dependencies and their licenses

---

## Testing Guidelines

- **Characterization tests**: Before refactoring, add tests that pass on old code
- **Pin exact error messages** in test expectations
- **Never use production credentials** - use fake adapters for integration tests
- **Isolate infrastructure**: If tests need external services, isolate them or mock them

### Adapter Testing

Use fake adapters for most testing:

```go
import "github.com/amibamae/gameserver-controller/internal/fakeadapter"

// Example
hostingAdapter := fakeadapter.NewVultr()
storageAdapter := fakeadapter.NewWasabi()
```

Fake adapters simulate provider behavior without actual cloud calls.

---

## Code Review Checklist

Before submitting a PR, verify:

- [ ] All tests pass (`go test -count=1 ./...`)
- [ ] No formatting issues (`gofmt -l .` prints nothing)
- [ ] Static analysis clean (`go vet ./...`)
- [ ] Protocol conformance passes
- [ ] Complexity unchanged for existing functions (or documented exception)
- [ ] New code has tests covering changed behavior
- [ ] Comments explain non-obvious logic
- [ ] No secrets or credentials in code

---

## Performance Considerations

The controller aims for sub-second operation responses. For performance-sensitive paths:

- Profile before optimizing
- Use benchmark tests (`go test -bench=.`)
- Avoid blocking I/O in hot paths
- Cache frequently-used adapter metadata

---

*Last updated: November 2026*