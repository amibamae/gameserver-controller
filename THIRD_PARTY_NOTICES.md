# Third-party notices

## bbolt

The embedded operation store uses [bbolt](https://github.com/etcd-io/bbolt), maintained by the etcd project and derived from Bolt by Ben Johnson.

- Module: `go.etcd.io/bbolt`
- Version: `v1.4.3`, pinned in [go.mod](go.mod)
- License: MIT
- Copyright: Copyright (c) 2013 Ben Johnson
- Full upstream notice: [licenses/bbolt-LICENSE](licenses/bbolt-LICENSE)

Include this notice and the full bbolt license with source or binary distributions that contain bbolt. Preserve upstream copyright and license notices when copying or modifying its source.

## Go runtime and standard library

Built Go executables include the Go runtime and the standard-library packages
they use.

- Toolchain: Go 1.23.12, as pinned in [go.mod](go.mod)
- License: Go license (BSD-style)
- Copyright: Copyright 2009 The Go Authors
- Full upstream notice: [licenses/go-LICENSE](licenses/go-LICENSE)

When distributing binaries containing Go runtime or standard-library code,
reproduce the copyright notice, license conditions, and disclaimer in the
documentation or other materials provided with the distribution. See
[the upstream Go license](https://go.dev/LICENSE).
