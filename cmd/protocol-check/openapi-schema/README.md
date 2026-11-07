# Vendored OpenAPI 3.2 schema resources

These files are copied from the OpenAPI Initiative's
[spec.openapis.org repository](https://github.com/OAI/spec.openapis.org/tree/main/oas/3.2).

- `oas-3.2-schema-2026-08-30.json`: OpenAPI 3.2 structural schema.
- `oas-3.2-schema-base-2026-08-30.json`: schema-base wrapper that validates OpenAPI Schema Objects using the OAS 3.2 dialect.
- `oas-3.2-dialect-2026-02-26.json` and `oas-3.2-meta-2026-02-26.json`: the OAS 3.2 JSON Schema dialect resources referenced by the wrapper.

The resources are included locally so protocol validation does not need network access. They are used under the Apache License, Version 2.0; a copy is in [LICENSE.txt](LICENSE.txt). The OpenAPI Initiative notes that its schemas catch many errors but do not guarantee detection of every specification violation; the normative specification text remains authoritative.
