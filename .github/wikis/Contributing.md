# Contributing Guide

Thanks for your interest in contributing to Game Server Controller! This guide helps you get involved, whether you're fixing bugs, adding features, or improving documentation.

---

## Before You Start

### Read These First

- **[About the Project](about-the-project)** - Understand what GSC does and why it exists
- **[Architecture Overview](architecture)** - Core vs adapter design patterns
- **[Security Model](security-model)** - Safety boundaries and threat model

### Work Protocol

1. **Create a feature branch** from main
2. **Run tests** before committing
3. **Use small, focused commits** - one logical change per commit
4. **Never commit secrets** or credentials to the repo
5. **Update documentation** as needed for your changes
6. **Submit a PR** after CI passes locally

### Code Health Limits

New and changed code should:

- Stay below cyclomatic complexity thresholds (see [CLAUDE.md](../CLAUDE.md))
- Include tests for non-obvious behavior
- Follow existing naming and formatting conventions
- Not increase memory footprint without justification

---

## Reporting Issues

### Before Creating an Issue

- Check existing issues to see if it's already reported
- Provide reproduction steps if possible
- Include your environment (OS, Go version, adapter configs)
- Link to relevant documentation or protocol specs

### Vulnerability Reports

If you find a security issue:

1. **Don't** create a public GitHub issue
2. Use GitHub's private vulnerability reporting for public repos
3. Or email the maintainer directly for private projects
4. Provide steps to reproduce, impact assessment, and suggested fix

---

## Making Changes

### Small Changes Are Fine

Typical contributions:

- Bug fixes
- Documentation improvements
- Minor refactoring with tests
- New adapter implementations

### Large Changes Need Discussion

Before starting work on significant features:

- Open a discussion in the issues or PR
- Get maintainer agreement on approach
- Document design decisions if needed
- Consider using [docs/ADRs/](../ADRs/) for architecture records

### Adding New Adapters

If you want to implement a new cloud provider or game engine adapter:

1. Follow existing adapter patterns in `internal/adapter/`
2. Use fake adapters for testing
3. Ensure it works with staging environment
4. Document any provider-specific limitations
5. Submit PR with comprehensive tests

---

## Documentation

### Writing Docs

- Keep docs practical and example-driven
- Include code snippets that actually work
- Reference other documentation where appropriate
- Update docs when changing behavior

### Adding Examples

Good examples:

- Minimal working configurations
- Common operation sequences
- Error handling patterns
- Provider-specific setup notes

Bad examples:

- Incomplete or broken code
- Hard-coded secrets or credentials
- Overly complex demonstrations

---

## Code Style

The project uses Go idioms:

```go
// Package names are lowercase, no underscores
package adapter

// Exported types start with capital letter
type HostingAdapter struct {
}

// Functions use verbs for clarity
func (a *HostingAdapter) Provision(ctx context.Context, params ProvisionParams) error {
    return nil
}

// Error handling with specific errors
if err != nil {
    return fmt.Errorf("failed to provision VM: %w", err)
}
```

Formatting:

- Run `gofmt -w .` before committing
- Add comments for non-obvious logic
- Keep functions focused and small

---

## Review Process

When you submit a PR:

1. Maintainer reviews code and tests
2. Feedback may include suggestions or fixes
3. Make requested changes to your branch
4. Re-run tests locally
5. Update PR description if needed
6. CI must pass before merge

### What Reviewers Look For

- Tests cover new behavior and edge cases
- No secrets or credentials exposed
- Documentation updated where needed
- Changes are minimal and focused
- Protocol conformance maintained

---

## Getting Help

Need guidance? Ask in:

- GitHub issues for questions
- PR reviews for code feedback  
- Documentation discussions

Remember: It's okay to make mistakes. The goal is learning while improving the project.

---

## License Attribution

When adding dependencies, remember to credit them in `THIRD_PARTY_NOTICES.md`. Include license type, version, and source URL for each package.

---

*Last updated: November 2026*