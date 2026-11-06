# AI Use and Development Transparency

**Status:** Public project disclosure  
**Last reviewed:** 2026-11-05

## Why I disclose this

I currently use AI-assisted tools while I design and prototype the project. If you evaluate, use, or contribute to open-source software, you should know where AI is involved and what role it will have in the finished product.

I do not want AI use hidden, overstated, or mistaken for a dependency of the controller itself.

## How I use AI during development

During architecture and proof-of-concept work, AI tools may help me:

- Explore and document the architecture.
- Draft or refine specifications, interfaces, and technical docs.
- Compare implementation options and their trade-offs.
- Prototype ideas and reason through workflows.
- Review configuration concepts, troubleshooting steps, and failure cases.
- Plan and organize development.

AI-assisted output is a starting point for human review. It is not automatically correct. Maintainers remain responsible for decisions, code, security, licensing, and the direction of the project.

This disclosure describes the current approach. It does not claim that AI generated every file, line of code, or contribution in this repository.

## AI will never be required

AI will never be required to build, install, configure, operate, maintain, or use this project.

The controller must work as ordinary software. Its core must not depend on an AI model or provider, an AI API key, an external AI service, a paid AI subscription, or network access to an AI service.

Provisioning, backup, restore, health checks, updates, staging, routing, and rollback must use deterministic, testable software, documented interfaces, and explicit operator policy. They must continue to work when AI services are unavailable.

I may use AI as a development aid. It will not become a required development tool, build dependency, runtime dependency, or operations service. Any future AI experiment, if we consider one, must remain optional, separately documented, and removable without affecting core behavior.

## Challenge AI-assisted work

I am open to simplifying or replacing AI-assisted designs and implementations with independently developed, conventional code. I expect proof-of-concept work to change as people review and test it.

If an AI-assisted approach is unnecessarily complex, hard to maintain, insecure, or poorly understood, I would rather replace it than keep it because it was quick to draft.

You can help by reviewing the architecture, proposing simpler implementations, replacing prototypes with tested code, identifying needless dependencies or abstractions, and improving docs so people can understand decisions without AI tools.

The goal is maintainable open-source software, not maximizing AI use.

## Human review and accountability

Plausible output is not verified output. Before maintainers accept AI-assisted code or designs, they must review and validate them. Depending on the change, that means:

- Understanding the behavior well enough to maintain it.
- Reviewing security, privacy, licensing, and dependency effects.
- Adding or updating tests.
- Exercising failure paths and destructive-action safeguards.
- Checking compatibility and documenting limits.
- Keeping secrets, private data, and unapproved third-party material out of the repository.

AI tools do not approve, merge, or release their own work.

## Contributors and AI

You do not need to use AI to contribute. Work written without AI assistance is equally welcome.

If AI materially helped with your contribution, you are encouraged to say which parts it assisted with and what you reviewed or tested. This supports transparency. It does not stigmatize AI-assisted work or replace ordinary code review.

All contributions follow the project’s licensing, security, testing, and review requirements in [CONTRIBUTING.md](../CONTRIBUTING.md).

## Ongoing commitment

The no-AI-requirement commitment is a standing project principle. I will keep this page reasonably current as development practices change.

Before adding an optional AI experiment or integration, maintainers must identify and document it. Where relevant, it must be off by default and designed so that removing AI services cannot impair the controller’s core behavior.

AI currently helps us explore and prototype. It will never be required to develop, build, install, configure, operate, maintain, or use the project. The software should remain open, understandable, and independently operable, and I welcome better human-designed and tested replacements for AI-assisted work.