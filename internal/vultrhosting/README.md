# vultrhosting

The read-only Vultr hosting adapter. The runtime builds it for an adapter profile with `implementation: vultr`. It uses the profile's `api_token` secret ref and the environment named in its `vultr:` block.

It implements `hosting.instance.inspect` and `hosting.instance.find_by_resource_key` through [`internal/vultrapi`](../vultrapi). It makes no mutating calls. See [the status page](../../docs/VULTR_ADAPTER_PROGRESS.md) for the state mapping and what is not implemented yet.

For tests that need a Vultr stand-in, use the scripted `internal/fakeadapter` test harness with a hosting descriptor rather than a new fake. See [docs/TEST_HARNESSES.md](../../docs/TEST_HARNESSES.md).
