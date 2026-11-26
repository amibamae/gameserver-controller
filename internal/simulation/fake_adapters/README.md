# Fake Adapters for Simulation Testing

This directory contains simulated infrastructure adapters for testing controller operations without real cloud resources.

## Adapters

- `vultr.go` - Vultr hosting API simulator (VM create/destroy, billing-aware timing)
- `wasabi.go` - Wasabi S3 storage simulator (object lifecycle, consistency checks)
- `papermc.go` - PaperMC server simulator (start/stop/save states via mcrcon-like interface)
- `systemd.go` - systemd service simulator (unit inspection, journal logs mock)
- `velocity.go` - Velocity proxy simulator (register/drain/transfer flows)

## Design Principles

1. **All operations are in-memory** - No network calls, no filesystem writes beyond test fixtures
2. **Deterministic by default** - Failures only when explicitly configured
3. **State tracking** - Every adapter maintains internal state for validation
4. **Schema-compliant responses** - All responses match protocol schemas

## Usage

```go
// Create environment with fake adapters
env, _ := simulation.NewEnvironment(simulation.DefaultConfig())
vultrSim := fakelvrt.New()
env.SetAdapter("vultr", vultrSim)

// Run test scenario
result, err := env.Execute(json.RawMessage(scenarioJSON))
```
