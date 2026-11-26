# Simulation Test Harness

This package provides isolated, deterministic test infrastructure for the Gameserver Controller without using real cloud resources.

## Purpose

- Develop and validate end-to-end lifecycle operations in simulation
- Implement fault injection testing (network failures, process crashes, storage errors)
- Test recovery scenarios and rollback paths
- Validate staging deployment flows and release gates
- All tests run with fake adapters - zero infrastructure charges

## Components

### Fake Infrastructure Adapters

See `internal/fakeadapter/` for existing scripted adapter implementation.

Additional simulators to be created:
- Vultr hosting simulator (VM create/destroy, billing-aware timing)
- Wasabi storage simulator (S3 object lifecycle, consistency checks)
- PaperMC server simulator (start/stop/save states)
- systemd service simulator (unit inspection, journal logs)
- Velocity proxy simulator (register/drain/transfer flows)

### Fault Injection Framework

To be implemented:
- Network failure injection (packet loss, timeout, connection refused)
- Process crash simulation (SIGSEGV, OOM kill scenarios)
- Storage failure injection (corrupted objects, read errors)
- Operation interruption testing (cancel running operations)

### Deterministic Test Orchestrator

To be implemented:
- Test scenario definitions (JSON schema)
- Step-by-step test execution engine
- Failure point annotations and replay support
- Result comparison with golden files

## Usage Pattern

```go
// Setup simulation environment
env := simulation.NewEnvironment(simulation.Config{
    Timeout: time.Minute * 10,
    FailRate: 0.0, // set to >0 for fault injection testing
})

// Register fake adapters
fakeVultr := fakeadapter.New(fakeadapter.Descriptor{...}).SetScript("hosting.create", replies...)
env.SetAdapter("vultr", fakeVultr)

// Define and run a test scenario
env.ExecuteScenario(json.RawMessage(scenarioJSON))
```

## Directory Structure

```
internal/simulation/
  ├── README.md                    # This file
  ├── orchestrator.go              # Test runner and scenario execution
  ├── fault_injection.go           # Deterministic failure injection
  ├── fake_adapters/
  │   ├── vultr.go                 # Vultr hosting simulator
  │   ├── wasabi.go                # Wasabi storage simulator
  │   ├── papermc.go               # PaperMC server simulator
  │   └── systemd.go              # systemd service simulator
  └── testfixtures/
      └── lifecycle.json           # End-to-end test scenarios
```

## Integration

The simulation harness integrates with:
- CI workflows via `internal/simulation` package tests
- Existing protocol conformance framework
- Fake adapter testing patterns in `internal/fakeadapter`
