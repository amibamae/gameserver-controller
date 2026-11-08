# OpenAPI 3.2 schema resources

This directory vendors four JSON schema resources from the OpenAPI Initiative's [OAS 3.2 publication repository](https://github.com/OAI/spec.openapis.org/tree/99da7018fa1a7768a9c5873e072f7fbc5924a7d3/oas/3.2), pinned to commit `99da7018fa1a7768a9c5873e072f7fbc5924a7d3`. We keep them here so protocol validation works without fetching schemas from the network.

- `oas-3.2-schema-2026-08-30.json` defines the OpenAPI 3.2 structure and matches upstream `oas/3.2/schema/2026-08-30`.
- `oas-3.2-schema-base-2026-08-30.json` validates OpenAPI Schema Objects using the OAS 3.2 dialect and matches upstream `oas/3.2/schema-base/2026-08-30`.
- `oas-3.2-dialect-2026-02-26.json` matches upstream `oas/3.2/dialect/2026-02-26`.
- `oas-3.2-meta-2026-02-26.json` matches upstream `oas/3.2/meta/2026-02-26`.

The files are distributed under Apache License 2.0; see [LICENSE.txt](LICENSE.txt). The schemas catch many structural errors, but they cannot establish that every rule in the OpenAPI specification has been followed. When the schema and normative specification text disagree, follow the specification.
