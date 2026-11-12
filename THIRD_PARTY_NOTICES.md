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

## Rust conformance consumer (`conformance-rs`)

[`conformance-rs`](conformance-rs) is an optional, independent test tool that runs in CI. It is not shipped with the controller and no binary is distributed, so these crates are not redistributed by this project. Versions match [conformance-rs/Cargo.lock](conformance-rs/Cargo.lock). Licenses are as declared in each crate's published metadata and checked against the license files in the published crate. `cargo test` in `conformance-rs` fails if a crate in the lockfile is missing from this table. Where a crate offers a choice of licenses, the project may use any of them.

The build uses the Rust toolchain (rustc and cargo, MIT OR Apache-2.0, by the Rust Project) installed on the CI runner.

| Crate | Version | License | Authors | Source | Notes |
|---|---|---|---|---|---|
| `ahash` | 0.8.12 | MIT OR Apache-2.0 | Tom Kaitchuck | [source](https://github.com/tkaitchuck/ahash) |  |
| `aho-corasick` | 1.1.5 | Unlicense OR MIT | Andrew Gallant | [source](https://github.com/BurntSushi/aho-corasick) |  |
| `allocator-api2` | 0.2.21 | MIT OR Apache-2.0 | Zakarum | [source](https://github.com/zakarumych/allocator-api2) |  |
| `autocfg` | 1.5.1 | Apache-2.0 OR MIT | Josh Stone | [source](https://github.com/cuviper/autocfg) |  |
| `bit-set` | 0.8.0 | Apache-2.0 OR MIT | Alexis Beingessner | [source](https://github.com/contain-rs/bit-set) |  |
| `bit-vec` | 0.8.0 | Apache-2.0 OR MIT | Alexis Beingessner | [source](https://github.com/contain-rs/bit-vec) |  |
| `bitflags` | 2.13.2 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/bitflags/bitflags) |  |
| `block-buffer` | 0.10.4 | MIT OR Apache-2.0 | RustCrypto Developers | [source](https://github.com/RustCrypto/utils) |  |
| `borrow-or-share` | 0.2.4 | MIT-0 | Scallop Ye | [source](https://github.com/yescallop/borrow-or-share) |  |
| `bumpalo` | 3.20.3 | MIT OR Apache-2.0 | Nick Fitzgerald | [source](https://github.com/fitzgen/bumpalo) | non-Linux targets only; not built in CI |
| `bytecount` | 0.6.9 | Apache-2.0/MIT | Andre Bogus, Joshua Landau | [source](https://github.com/llogiq/bytecount) |  |
| `cfg-if` | 1.0.5 | MIT OR Apache-2.0 | Alex Crichton | [source](https://github.com/rust-lang/cfg-if) |  |
| `cpufeatures` | 0.2.17 | MIT OR Apache-2.0 | RustCrypto Developers | [source](https://github.com/RustCrypto/utils) |  |
| `crypto-common` | 0.1.7 | MIT OR Apache-2.0 | RustCrypto Developers | [source](https://github.com/RustCrypto/traits) |  |
| `data-encoding` | 2.11.1 | MIT | see repository | [source](https://github.com/ia0/data-encoding) |  |
| `digest` | 0.10.7 | MIT OR Apache-2.0 | RustCrypto Developers | [source](https://github.com/RustCrypto/traits) |  |
| `email_address` | 0.2.9 | MIT | Simon Johnston | [source](https://github.com/johnstonskj/rust-email_address) |  |
| `equivalent` | 1.0.3 | Apache-2.0 OR MIT | see repository | [source](https://github.com/indexmap-rs/equivalent) |  |
| `fancy-regex` | 0.20.2 | MIT | Raph Levien, Robin Stocker, Keith Hall | [source](https://github.com/fancy-regex/fancy-regex) |  |
| `fluent-uri` | 0.4.1 | MIT | Scallop Ye | [source](https://github.com/yescallop/fluent-uri-rs) |  |
| `foldhash` | 0.2.0 | Zlib | Orson Peters | [source](https://github.com/orlp/foldhash) |  |
| `fraction` | 0.17.0 | MIT OR Apache-2.0 | dnsl48 | [source](https://github.com/dnsl48/fraction) |  |
| `generic-array` | 0.14.7 | MIT | Bartłomiej Kamiński, Aaron Trent | [source](https://github.com/fizyk20/generic-array) |  |
| `getrandom` | 0.3.4 | MIT OR Apache-2.0 | The Rand Project Developers | [source](https://github.com/rust-random/getrandom) |  |
| `hashbrown` | 0.17.1 | MIT OR Apache-2.0 | see repository | [source](https://github.com/rust-lang/hashbrown) |  |
| `heck` | 0.5.0 | MIT OR Apache-2.0 | see repository | [source](https://github.com/withoutboats/heck) |  |
| `itoa` | 1.0.19 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/itoa) |  |
| `js-sys` | 0.3.107 | MIT OR Apache-2.0 | The wasm-bindgen Developers | [source](https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/js-sys) | non-Linux targets only; not built in CI |
| `jsonschema` | 0.61.1 | MIT | Dmitry Dygalo | [source](https://github.com/Stranger6667/jsonschema) |  |
| `jsonschema-regex` | 0.61.1 | MIT | Dmitry Dygalo | [source](https://github.com/Stranger6667/jsonschema) | no license file in the published crate; license from crate metadata |
| `jsonschema-value` | 0.61.1 | MIT | Dmitry Dygalo | [source](https://github.com/Stranger6667/jsonschema) | no license file in the published crate; license from crate metadata |
| `libc` | 0.2.191 | MIT OR Apache-2.0 | see repository | [source](https://github.com/rust-lang/libc) |  |
| `lock_api` | 0.4.14 | MIT OR Apache-2.0 | Amanieu d'Antras | [source](https://github.com/Amanieu/parking_lot) |  |
| `memchr` | 2.8.3 | Unlicense OR MIT | Andrew Gallant, bluss | [source](https://github.com/BurntSushi/memchr) |  |
| `micromap` | 0.3.0 | MIT | see repository | [source](https://github.com/yegor256/micromap) |  |
| `num` | 0.4.3 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num) |  |
| `num-bigint` | 0.4.8 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-bigint) |  |
| `num-cmp` | 0.1.0 | MIT/Apache-2.0 | Kang Seonghoon | [source](https://github.com/lifthrasiir/num-cmp) |  |
| `num-complex` | 0.4.6 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-complex) |  |
| `num-integer` | 0.1.47 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-integer) |  |
| `num-iter` | 0.1.46 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-iter) |  |
| `num-rational` | 0.4.2 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-rational) |  |
| `num-traits` | 0.2.19 | MIT OR Apache-2.0 | The Rust Project Developers | [source](https://github.com/rust-num/num-traits) |  |
| `once_cell` | 1.21.4 | MIT OR Apache-2.0 | Aleksey Kladov | [source](https://github.com/matklad/once_cell) |  |
| `outref` | 0.5.2 | MIT | see repository | [source](https://github.com/Nugine/outref) |  |
| `parking_lot` | 0.12.5 | MIT OR Apache-2.0 | Amanieu d'Antras | [source](https://github.com/Amanieu/parking_lot) |  |
| `parking_lot_core` | 0.9.12 | MIT OR Apache-2.0 | Amanieu d'Antras | [source](https://github.com/Amanieu/parking_lot) |  |
| `percent-encoding` | 2.3.2 | MIT OR Apache-2.0 | The rust-url developers | [source](https://github.com/servo/rust-url) |  |
| `proc-macro2` | 1.0.107 | MIT OR Apache-2.0 | David Tolnay, Alex Crichton | [source](https://github.com/dtolnay/proc-macro2) |  |
| `quote` | 1.0.47 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/quote) |  |
| `r-efi` | 5.3.0 | MIT OR Apache-2.0 OR LGPL-2.1-or-later | see repository | [source](https://github.com/r-efi/r-efi) | no license file in the published crate; license from crate metadata; non-Linux targets only; not built in CI |
| `redox_syscall` | 0.5.18 | MIT | Jeremy Soller | [source](https://gitlab.redox-os.org/redox-os/syscall) | non-Linux targets only; not built in CI |
| `ref-cast` | 1.0.27 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/ref-cast) |  |
| `ref-cast-impl` | 1.0.27 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/ref-cast) |  |
| `referencing` | 0.61.1 | MIT | Dmitry Dygalo | [source](https://github.com/Stranger6667/jsonschema) |  |
| `regex` | 1.13.1 | MIT OR Apache-2.0 | The Rust Project Developers, Andrew Gallant | [source](https://github.com/rust-lang/regex) |  |
| `regex-automata` | 0.4.18 | MIT OR Apache-2.0 | The Rust Project Developers, Andrew Gallant | [source](https://github.com/rust-lang/regex) |  |
| `regex-syntax` | 0.8.11 | MIT OR Apache-2.0 | The Rust Project Developers, Andrew Gallant | [source](https://github.com/rust-lang/regex) |  |
| `rustversion` | 1.0.23 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/rustversion) |  |
| `ryu-js` | 1.0.3 | Apache-2.0 OR BSL-1.0 (used under Apache-2.0) | David Tolnay, boa-dev | [source](https://github.com/boa-dev/ryu-js) | ECMAScript number formatting for RFC 8785 JCS in `conformance-rs`. License checked in the crate's `Cargo.toml` and `LICENSE-APACHE`. |
| `scopeguard` | 1.2.0 | MIT OR Apache-2.0 | bluss | [source](https://github.com/bluss/scopeguard) |  |
| `serde` | 1.0.229 | MIT OR Apache-2.0 | Erick Tryzelaar, David Tolnay | [source](https://github.com/serde-rs/serde) |  |
| `serde_core` | 1.0.229 | MIT OR Apache-2.0 | Erick Tryzelaar, David Tolnay | [source](https://github.com/serde-rs/serde) |  |
| `serde_derive` | 1.0.229 | MIT OR Apache-2.0 | Erick Tryzelaar, David Tolnay | [source](https://github.com/serde-rs/serde) |  |
| `serde_json` | 1.0.154 | MIT OR Apache-2.0 | see repository | [source](https://github.com/serde-rs/json) |  |
| `sha2` | 0.10.9 | MIT OR Apache-2.0 | RustCrypto Developers | [source](https://github.com/RustCrypto/hashes) |  |
| `smallvec` | 1.16.5 | MIT OR Apache-2.0 | The Servo Project Developers | [source](https://github.com/servo/rust-smallvec) |  |
| `strum` | 0.28.0 | MIT | Peter Glotfelty | [source](https://github.com/Peternator7/strum) |  |
| `strum_macros` | 0.28.0 | MIT | Peter Glotfelty | [source](https://github.com/Peternator7/strum) |  |
| `syn` | 2.0.119 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/syn) |  |
| `syn` | 3.0.10 | MIT OR Apache-2.0 | David Tolnay | [source](https://github.com/dtolnay/syn) |  |
| `typenum` | 1.20.1 | MIT OR Apache-2.0 | see repository | [source](https://github.com/paholg/typenum) |  |
| `unicode-general-category` | 1.1.0 | Apache-2.0 | YesLogic Pty. Ltd. | [source](https://github.com/yeslogic/unicode-general-category) |  |
| `unicode-ident` | 1.0.27 | (MIT OR Apache-2.0) AND Unicode-3.0 | David Tolnay | [source](https://github.com/dtolnay/unicode-ident) |  |
| `uuid-simd` | 0.8.0 | MIT | see repository | [source](https://github.com/Nugine/simd) | no license file in the published crate; license from crate metadata |
| `version_check` | 0.9.5 | MIT/Apache-2.0 | Sergio Benitez | [source](https://github.com/SergioBenitez/version_check) |  |
| `vsimd` | 0.8.0 | MIT | see repository | [source](https://github.com/Nugine/simd) | no license file in the published crate; license from crate metadata |
| `wasip2` | 1.0.4+wasi-0.2.12 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT | see repository | [source](https://github.com/bytecodealliance/wasi-rs) | non-Linux targets only; not built in CI |
| `wasm-bindgen` | 0.2.130 | MIT OR Apache-2.0 | The wasm-bindgen Developers | [source](https://github.com/wasm-bindgen/wasm-bindgen) | non-Linux targets only; not built in CI |
| `wasm-bindgen-macro` | 0.2.130 | MIT OR Apache-2.0 | The wasm-bindgen Developers | [source](https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/macro) | non-Linux targets only; not built in CI |
| `wasm-bindgen-macro-support` | 0.2.130 | MIT OR Apache-2.0 | The wasm-bindgen Developers | [source](https://github.com/wasm-bindgen/wasm-bindgen/tree/main/crates/macro-support) | non-Linux targets only; not built in CI |
| `wasm-bindgen-shared` | 0.2.130 | MIT OR Apache-2.0 | The wasm-bindgen Developers | [source](https://github.com/wasm-bindgen/wasm-bindgen/tree/master/crates/shared) | non-Linux targets only; not built in CI |
| `windows-link` | 0.2.1 | MIT OR Apache-2.0 | see repository | [source](https://github.com/microsoft/windows-rs) | no license file in the published crate; license from crate metadata; non-Linux targets only; not built in CI |
| `wit-bindgen` | 0.57.1 | Apache-2.0 WITH LLVM-exception OR Apache-2.0 OR MIT | Alex Crichton | [source](https://github.com/bytecodealliance/wit-bindgen) | non-Linux targets only; not built in CI |
| `zerocopy` | 0.8.63 | BSD-2-Clause OR Apache-2.0 OR MIT | see repository | [source](https://github.com/google/zerocopy) |  |
| `zerocopy-derive` | 0.8.63 | BSD-2-Clause OR Apache-2.0 OR MIT | see repository | [source](https://github.com/google/zerocopy) |  |
| `zmij` | 1.0.23 | MIT | David Tolnay | [source](https://github.com/dtolnay/zmij) |  |
