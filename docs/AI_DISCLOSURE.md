# AI Use and Development Transparency

**Status:** Public project disclosure  
**Last reviewed:** 2026-11-12

## Why I disclose this

AI coding agents write most of this project. I direct the work, make the design decisions, and decide what ships. If you evaluate, use, or contribute to open-source software, you should know where AI is involved and what role it will have in the finished product.

I do not want AI use hidden, overstated, or mistaken for a dependency of the controller itself.

## How much AI is used

Nearly all of the repository was written by AI coding agents working from my instructions. That includes the Go controller and its tests, the protocol schemas, examples and conformance vectors, the Java Velocity plugin, the Rust conformance consumer (`conformance-rs/`), the CI workflows, and the docs.

Measured from the git history and GitHub on 2026-11-12, from the first commit (2026-11-07):

| Measure | Count |
|---|---|
| Merged pull requests | 166 |
| From `codex/…` branches (OpenAI Codex agent) | 154 |
| From `claude/…` branches (Anthropic Claude Code agent) | 11 |
| From other branches | 1 |
| Non-merge commits on `main` | 201 |
| Commits with a `Co-Authored-By: Claude …` trailer | 37 |

Codex commits are recorded under my name without a trailer, so the commit authors alone understate AI use. Treat the branch prefix as the marker. Claude Code commits carry the trailer and a `Claude-Session` link.

To refresh the counts:

```bash
git log origin/main --no-merges --oneline | wc -l
git log origin/main --no-merges -i --grep='Co-Authored-By: Claude' --oneline | wc -l
```

The branch counts come from the repository's closed pull requests on GitHub.

## What AI does and what I do

The agents:

- Write code, tests, protocol files, and docs, and run the repository's checks (`gofmt`, `go vet`, `go test`, `protocol-check`, and the Rust checks).
- Plan and order work, open pull requests, and rebase branches.
- Merge their own pull requests once CI is green, in sessions where I have authorized that.
- Hand work to each other through [AGENT_HANDOFF.md](AGENT_HANDOFF.md) when a session ends.
- Follow the standing instructions in [CLAUDE.md](../CLAUDE.md), including crediting every third-party dependency in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

I:

- Choose what to build, set the requirements, and make the design decisions the agents ask about.
- Authorize merges, force-pushes, and anything that touches live systems.
- Run live validation against real providers myself (a real Velocity proxy, a Cloudflare staging zone, Vultr billing).

Automated checks and CI are the main gate on AI-written changes. Not every line has had a separate line-by-line human review. AI output is not automatically correct, and I remain responsible for the code, its security and licensing, and the direction of the project.

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

Some of these steps are not yet done for every AI-written change; see [What AI does and what I do](#what-ai-does-and-what-i-do). AI agents may merge their own pull requests after CI passes when I have authorized it. They do not approve their own pull requests through review, and they do not publish a release or protocol tag without my explicit go-ahead.

## Contributors and AI

You do not need to use AI to contribute. Work written without AI assistance is equally welcome.

If AI materially helped with your contribution, you are encouraged to say which parts it assisted with and what you reviewed or tested. This supports transparency. It does not stigmatize AI-assisted work or replace ordinary code review.

All contributions follow the project’s licensing, security, testing, and review requirements in [CONTRIBUTING.md](../CONTRIBUTING.md).

## Ongoing commitment

The no-AI-requirement commitment is a standing project principle. I will keep this page reasonably current as development practices change.

Before adding an optional AI experiment or integration, maintainers must identify and document it. Where relevant, it must be off by default and designed so that removing AI services cannot impair the controller’s core behavior.

AI currently writes most of the project. It will never be required to develop, build, install, configure, operate, maintain, or use the project. The software should remain open, understandable, and independently operable, and I welcome better human-designed and tested replacements for AI-assisted work.