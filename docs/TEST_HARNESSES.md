# Test harnesses

A test harness is a Go package that exists to help tests: a scripted fake adapter, a simulator, or shared fixtures. Harnesses live in the repository so every contributor can use them. They must never become part of the controller that runs on a real host.

## Rules

1. **Label it.** The package doc of a harness contains this line, exactly:

   ```go
   // TEST HARNESS: for tests only. Production code must not import this package.
   ```

2. **List it.** Add its path to `Packages` in [`internal/testharness/testharness.go`](../internal/testharness/testharness.go) and to the table below.
3. **Import it only from tests.** Only `_test.go` files and other harnesses may import a harness. Never register a harness as an adapter implementation in `internal/controllerruntime`, and never select one from configuration.
4. **Keep it offline.** A harness makes no network, provider, or process calls, and it reads no real credentials.

`go test ./internal/testharness` enforces rules 1 to 3. It fails when a listed package lacks the label, when a labeled package is missing from `Packages` or from this page, or when non-test code imports a harness.

## Available harnesses

| Package | Use it for |
| --- | --- |
| `internal/fakeadapter` | Scripted in-process adapters for every adapter category: hosting, storage, execution, game, and integration. `fakeadapter.New(descriptor)` returns an adapter whose replies you script per capability with `SetScript`; `Calls` returns the requests it received. Replies run through the same schema-validating registry as real adapters, so outputs must match the protocol schemas. For example, use a hosting descriptor to stand in for Vultr. |
| `internal/simulation` | The deterministic simulation core: a virtual clock, a fault-injection plan, a safety-invariant checker, and seeded randomness. Per-backend simulators build on it. It never reads the wall clock or the global random source. |

## Adding a harness

Write the package under `internal/`, name it after what it fakes, give its package doc the label line, add it to `Packages` and to the table above, and cover it with tests of its own. If it fakes a provider, make its inputs and outputs match the protocol schemas, as `internal/fakeadapter` does, so tests that pass against the harness also hold against the real adapter.
