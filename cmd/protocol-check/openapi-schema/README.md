# OpenAPI 3.2 schema resources

This directory vendors the OpenAPI Initiative's [OAS 3.2 schema files](https://github.com/OAI/spec.openapis.org/tree/main/oas/3.2). We keep them here so protocol validation works without fetching schemas from the network.

- `oas-3.2-schema-2026-08-30.json` defines the OpenAPI 3.2 structure.
- `oas-3.2-schema-base-2026-08-30.json` validates OpenAPI Schema Objects using the OAS 3.2 dialect.
- `oas-3.2-dialect-2026-02-26.json` and `oas-3.2-meta-2026-02-26.json` provide the dialect resources the wrapper references.

The files are distributed under Apache License 2.0; see [LICENSE.txt](LICENSE.txt). The schemas catch many structural errors, but they cannot establish that every rule in the OpenAPI specification has been followed. When the schema and normative specification text disagree, follow the specification.