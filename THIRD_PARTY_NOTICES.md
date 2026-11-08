# Third-party notices

For source or binary distributions that include a component below, include this file and its linked license and attribution material with the distribution. Go module versions match [go.mod](go.mod). Test-only modules recorded only in go.sum are omitted because their source is not vendored or linked into project binaries.

## OpenAPI Initiative OAS 3.2 schema resources

The four JSON resources in `cmd/protocol-check/openapi-schema/` are byte-for-byte copies of files from the [OpenAPI Initiative publication repository at commit 99da7018fa1a7768a9c5873e072f7fbc5924a7d3](https://github.com/OAI/spec.openapis.org/tree/99da7018fa1a7768a9c5873e072f7fbc5924a7d3/oas/3.2). They are licensed under Apache License 2.0. The full license is included at [cmd/protocol-check/openapi-schema/LICENSE.txt](cmd/protocol-check/openapi-schema/LICENSE.txt). The upstream repository has no NOTICE file at that revision.

- `oas-3.2-schema-2026-08-30.json` corresponds to upstream `oas/3.2/schema/2026-08-30`.
- `oas-3.2-schema-base-2026-08-30.json` corresponds to upstream `oas/3.2/schema-base/2026-08-30`.
- `oas-3.2-dialect-2026-02-26.json` corresponds to upstream `oas/3.2/dialect/2026-02-26`.
- `oas-3.2-meta-2026-02-26.json` corresponds to upstream `oas/3.2/meta/2026-02-26`.

## bbolt

The embedded operation store uses [bbolt](https://github.com/etcd-io/bbolt), maintained by the etcd project and derived from Bolt by Ben Johnson.

- Module: `go.etcd.io/bbolt`
- Version: `v1.4.3`, pinned in [go.mod](go.mod)
- License: MIT
- Copyright: Copyright (c) 2013 Ben Johnson
- Full upstream notice: [licenses/bbolt-LICENSE](licenses/bbolt-LICENSE)

## Go runtime and standard library

Built Go executables include the Go runtime and the standard-library packages they use.

- Toolchain: Go 1.23.12, as pinned in [go.mod](go.mod)
- License: Go license (BSD-style)
- Copyright: Copyright 2009 The Go Authors
- Full upstream notice: [licenses/go-LICENSE](licenses/go-LICENSE)

When distributing binaries containing Go runtime or standard-library code, reproduce the copyright notice, license conditions, and disclaimer in the documentation or other materials provided with the distribution. See [the upstream Go license](https://go.dev/LICENSE).

## github.com/dlclark/regexp2

- Version: `v1.11.0`, pinned in [go.mod](go.mod)
- Upstream: [github.com/dlclark/regexp2](https://github.com/dlclark/regexp2/tree/v1.11.0)
- License: MIT
- Copyright: Doug Clark
- Full license: [licenses/regexp2-LICENSE](licenses/regexp2-LICENSE)

## github.com/gowebpki/jcs

- Version: `v1.0.2`, pinned in [go.mod](go.mod)
- Upstream: [github.com/gowebpki/jcs](https://github.com/gowebpki/jcs/tree/v1.0.2)
- License: Apache License 2.0
- Copyright notices in the upstream Go source: Copyright 2021-2026 Bret Jordan and Benedikt Thoma; Copyright 2006-2019 WebPKI.org
- Original-work credit: The upstream README credits Anders Rundgren as the creator of the original JCS implementation and says this Go fork was made with his permission.
- Full license: [licenses/jcs-LICENSE](licenses/jcs-LICENSE)

## github.com/santhosh-tekuri/jsonschema/v6

- Version: `v6.0.3`, pinned in [go.mod](go.mod)
- License: Apache License 2.0
- Upstream source: [github.com/santhosh-tekuri/jsonschema](https://github.com/santhosh-tekuri/jsonschema/tree/v6.0.3)
- Full license: [licenses/jsonschema-LICENSE](licenses/jsonschema-LICENSE)

## golang.org/x/sys

- Version: `v0.29.0`, pinned in [go.mod](go.mod)
- Upstream: [golang.org/x/sys](https://github.com/golang/sys/tree/v0.29.0)
- License: BSD 3-Clause
- Copyright: Copyright 2009 The Go Authors
- The pinned upstream license text matches [licenses/go-LICENSE](licenses/go-LICENSE) exactly; include that full text for x/sys as well.

## golang.org/x/text

- Version: `v0.14.0`, pinned in [go.mod](go.mod)
- Upstream: [golang.org/x/text](https://github.com/golang/text/tree/v0.14.0)
- License: BSD 3-Clause
- Copyright: Copyright (c) 2009 The Go Authors. All rights reserved.
- Full license: [licenses/x-text-LICENSE](licenses/x-text-LICENSE)

## gopkg.in/yaml.v3

- Version: `v3.0.1`, pinned in [go.mod](go.mod)
- Upstream: [go-yaml/yaml](https://github.com/go-yaml/yaml/tree/v3.0.1)
- License: MIT and Apache License 2.0; see the upstream license for file-level scope
- Copyright notices: Canonical Ltd and Kirill Simonov, as stated in the upstream license
- Full license: [licenses/yaml-LICENSE](licenses/yaml-LICENSE)
- Upstream NOTICE: [licenses/yaml-NOTICE](licenses/yaml-NOTICE)

Preserve both the license and NOTICE when distributing a build that includes this module.
