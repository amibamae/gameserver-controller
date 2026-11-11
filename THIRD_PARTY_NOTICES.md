# Third-party notices

For source or binary distributions that include a component below, include this file and its linked license and attribution material with the distribution. Go module versions match [go.mod](go.mod). Test-only modules recorded only in go.sum are omitted because their source is not vendored or linked into project binaries. The repository's AGPL-3.0 license does not replace the separate terms attached to these components.

## bbolt

The embedded operation store uses [bbolt](https://github.com/etcd-io/bbolt), maintained by the etcd project and derived from Bolt by Ben Johnson.

- Module: `go.etcd.io/bbolt`
- Version: `v1.4.3`, pinned in [go.mod](go.mod)
- License: MIT
- Copyright: Copyright (c) 2013 Ben Johnson
- Full upstream notice: [licenses/bbolt-LICENSE](licenses/bbolt-LICENSE)

## AWS SDK for Go v2

The Wasabi storage adapter uses the AWS SDK for Go v2, including its S3 client,
transfer manager, credential provider, and event-stream modules.

- Modules and versions: `github.com/aws/aws-sdk-go-v2 v1.39.4`,
  `github.com/aws/aws-sdk-go-v2/credentials v1.18.19`,
  `github.com/aws/aws-sdk-go-v2/feature/s3/manager v1.20.0`,
  `github.com/aws/aws-sdk-go-v2/service/s3 v1.88.7`, and
  `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.2`, pinned in
  [go.mod](go.mod)
- Upstream: [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2)
- License: Apache License 2.0
- Copyright: Copyright 2015 Amazon.com, Inc. or its affiliates. All Rights
  Reserved. Copyright 2014-2015 Stripe, Inc.
- Full license: [licenses/aws-sdk-go-v2-LICENSE](licenses/aws-sdk-go-v2-LICENSE)
- Upstream notice: [licenses/aws-sdk-go-v2-NOTICE](licenses/aws-sdk-go-v2-NOTICE)

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

## github.com/gowebpki/jcs

This project uses the Go implementation of RFC 8785 JSON Canonicalization Scheme for protocol conformance checks.

- Module: `github.com/gowebpki/jcs`
- Version: `v1.0.2`, pinned in [go.mod](go.mod) and [go.sum](go.sum)
- License: Apache-2.0
- Copyright: Copyright 2021-2026 Bret Jordan & Benedikt Thoma; Copyright 2006-2019 WebPKI.org
- Full upstream license: [licenses/gowebpki-jcs-LICENSE](licenses/gowebpki-jcs-LICENSE)
- Upstream NOTICE file: none at `v1.0.2`

Include this notice and the full Apache 2.0 license with source or binary distributions that contain this module. Preserve upstream copyright notices when copying or modifying its source.

## Go runtime and standard library

Built Go executables include the Go runtime and the standard-library packages they use.

- Toolchain: Go 1.23.12, as pinned in [go.mod](go.mod)
- License: Go license (BSD-style)
- Copyright: Copyright 2009 The Go Authors
- Full upstream notice: [licenses/go-LICENSE](licenses/go-LICENSE)

When distributing binaries containing Go runtime or standard-library code, reproduce the copyright notice, license conditions, and disclaimer in the documentation or other materials provided with the distribution. See [the upstream Go license](https://go.dev/LICENSE).

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
## OpenAPI Initiative OAS 3.2 schema resources

The four JSON resources in `cmd/protocol-check/openapi-schema/` are byte-for-byte copies of files from the [OpenAPI Initiative publication repository at commit 99da7018fa1a7768a9c5873e072f7fbc5924a7d3](https://github.com/OAI/spec.openapis.org/tree/99da7018fa1a7768a9c5873e072f7fbc5924a7d3/oas/3.2). They are licensed under Apache License 2.0. The full license is included at [cmd/protocol-check/openapi-schema/LICENSE.txt](cmd/protocol-check/openapi-schema/LICENSE.txt). The upstream repository has no NOTICE file at that revision.

- `oas-3.2-schema-2026-08-30.json` corresponds to upstream `oas/3.2/schema/2026-08-30`.
- `oas-3.2-schema-base-2026-08-30.json` corresponds to upstream `oas/3.2/schema-base/2026-08-30`.
- `oas-3.2-dialect-2026-02-26.json` corresponds to upstream `oas/3.2/dialect/2026-02-26`.
- `oas-3.2-meta-2026-02-26.json` corresponds to upstream `oas/3.2/meta/2026-02-26`.


## Velocity bridge plugin

The [Velocity bridge plugin](plugins/velocity) compiles against the Velocity API and the libraries below. It declares them `compileOnly`, and the running Velocity proxy supplies them at runtime, so the plugin jar bundles no third-party code. Licenses were checked at the sources named here in November 2026.

- **Velocity API**: `com.velocitypowered:velocity-api:3.4.0`, from the PaperMC Maven repository.
  - License: MIT. Copyright 2018 Velocity Contributors. Full text: [licenses/velocity-api-LICENSE](licenses/velocity-api-LICENSE), copied from [`api/LICENSE` in PaperMC/Velocity](https://github.com/PaperMC/Velocity/blob/HEAD/api/LICENSE).
  - The published 3.4.0 POM and jar carry no license text, and the upstream `api/LICENSE` was not reachable at the 3.4.0 tag, so the MIT grant above comes from the repository's current `api/LICENSE`. Re-check it at the tag when it is accessible.
  - The Velocity proxy itself is GPLv3 ([PaperMC/Velocity](https://github.com/PaperMC/Velocity)). The plugin links only the API module and does not include proxy code.
- **Gson 2.10.1** (`com.google.code.gson:gson`): Apache-2.0, by Google. [LICENSE](https://github.com/google/gson/blob/gson-parent-2.10.1/LICENSE).
- **toml4j 0.7.2** (`com.moandjiezana.toml:toml4j`): MIT, by Moandji Ezana (license named in its [published POM](https://repo1.maven.org/maven2/com/moandjiezana/toml/toml4j/0.7.2/toml4j-0.7.2.pom)).
- **SLF4J 2.0.17** (`org.slf4j:slf4j-api`): MIT, Copyright (c) 2004-2022 QOS.ch Sarl (Switzerland). [LICENSE.txt](https://github.com/qos-ch/slf4j/blob/v_2.0.17/LICENSE.txt).
- **Guice 6.0.0** (`com.google.inject:guice`, `@Inject` only): Apache-2.0, by Google, per its [parent POM](https://repo1.maven.org/maven2/com/google/inject/guice-parent/6.0.0/guice-parent-6.0.0.pom) and [COPYING](https://github.com/google/guice/blob/6.0.0/COPYING).
- **JUnit 5.11.4** (`org.junit:junit-bom`, `org.junit.jupiter:junit-jupiter`, `org.junit.platform:junit-platform-launcher`): Eclipse Public License 2.0, by the JUnit team ([POM](https://repo1.maven.org/maven2/org/junit/jupiter/junit-jupiter/5.11.4/junit-jupiter-5.11.4.pom)). Used for tests only and not included in the jar.

Distributors of a Velocity proxy bundle follow Velocity's own license terms.

## CI and build tooling

These run in GitHub Actions workflows and are not distributed with the project or its binaries.

- **GitHub Actions** (all MIT, Copyright (c) 2018 GitHub, Inc. and contributors): [`actions/checkout@v4`](https://github.com/actions/checkout), [`actions/setup-go@v5`](https://github.com/actions/setup-go), and [`actions/setup-java@v4`](https://github.com/actions/setup-java). Text: [licenses/github-actions-LICENSE](licenses/github-actions-LICENSE).
- **`gradle/actions/setup-gradle@v4`**: MIT, Copyright (c) 2023 Gradle Inc. ([LICENSE](https://github.com/gradle/actions/blob/v4/LICENSE)). Later major versions bundle a proprietary "Enhanced Caching" component (`gradle-actions-caching`) under Gradle's own terms and enable it by default. The `v4` tag has no such component or license file, and this project does not use a later version. Check this before upgrading.
- **Gradle 8.14.3**, pinned in `.github/workflows/velocity-plugin.yml`: Apache-2.0, by Gradle Inc. ([LICENSE](https://github.com/gradle/gradle/blob/v8.14.3/LICENSE)).
- **Eclipse Temurin JDK 21**, installed by `actions/setup-java` with `distribution: temurin`: GNU GPL v2 with the Classpath Exception, from the Eclipse Adoptium project ([license](https://github.com/adoptium/jdk21u/blob/HEAD/LICENSE)). The JDK is only the build and test toolchain.
