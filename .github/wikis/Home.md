# Game Server Controller Wiki

Welcome to the Game Server Controller (GSC) wiki!

## About This Project

Game Server Controller is a lifecycle controller for game servers, specifically designed to manage Minecraft Java servers running Paper.

**Core Purpose:**
> Bring a backend online when players need it, restore the world, send players across only after the game is ready, and remove the VM only after a fresh backup has been verified.

### Reference Stack

- **Cloud Hosting:** Vultr (disposable VMs)
- **Object Storage:** Wasabi (S3-compatible backups)
- **Game Engine:** Minecraft Java with Paper
- **Proxy:** Velocity (player-facing proxy)
- **Operating System:** Debian (via versioned bootstrap scripts)

### Key Principles

- **Provider-Agnostic Core** - Add new providers without rewriting the core
- **Language-Neutral Protocol** - Wire protocol in JSON Schema; Go is just the first implementation
- **Protocol-First** - The contract comes first; any language can implement adapters
- **Privacy by Default** - Telemetry off by default; player consent required

### Current Status

**Status:** In Development - Phase 1 Protocol Complete

- ✅ Protocol schemas (`0.21.0-draft`) reviewed
- ✅ Durable operation model implemented
- ✅ Workflow engine with recovery
- 🚧 Production adapters being wired
- 🔮 Future: additional providers, auto-scaling, staging

---

## Quick Links

### 📖 Project Documentation

| Topic | Page |
|-------|------|
| [About the Project](About-the-Project) | What GSC is and why it exists |
| [Architecture Overview](Architecture) | Core vs adapters, component diagram |
| [Quick Start Guide](Quick-Start) | Setup commands and getting started |
| [Configuration Guide](Configuration) | YAML schema reference |
| [Protocol Specification](Protocol-Spec) | JSON Schema protocol definition |
| [Security Model](Security-Model) | Threat boundaries, controls |
| [Implementation Status](Implementation-Status) | What's done, what's next |
| [Development Workflow](Development) | Branching, tests, commits |

### 🤝 Community & Contribution

| Topic | Page |
|-------|------|
| [Contributing Guide](Contributing) | How to contribute |
| [Code of Conduct](Code-of-Conduct) | Community guidelines |
| [Third-Party Notices](Third-Party-Notices) | License credits |

### 📚 Detailed Documentation (docs/)

For in-depth technical reference, see the `docs/` directory for:
- Architecture deep dive
- Adapter contracts
- Decision records (ADRs)
- Security model details
- Configuration schema
- And more...

---

## Getting Started

### New to GSC? Read Here:

1. **[About the Project](About-the-Project)** - Understand what this project does
2. **[Quick Start Guide](Quick-Start)** - Install and run locally
3. **[Configuration Guide](Configuration)** - Set up your environment

### Looking to Contribute?

- Check **[Contributing Guide](Contributing)** for guidelines
- Read **[Code of Conduct](Code-of-Conduct)** before participating
- Review **[Protocol Specification](Protocol-Spec)** if implementing adapters

---

## Wiki Navigation

Use the sidebar on the left to navigate, or jump directly to:

- [Home](/)
- [About the Project](About-the-Project)
- [Architecture Overview](Architecture)
- [Quick Start Guide](Quick-Start)
- [Configuration Guide](Configuration)
- [Protocol Specification](Protocol-Spec)
- [Security Model](Security-Model)
- [Implementation Status](Implementation-Status)
- [Development Workflow](Development)
- [Contributing Guide](Contributing)
- [Code of Conduct](Code-of-Conduct)

---

*Last updated: November 2026*
