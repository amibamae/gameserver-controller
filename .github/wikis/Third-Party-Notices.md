# Third-Party Notices

This document lists all third-party software used by Game Server Controller and their license credits. When you add, upgrade, or remove dependencies, update this file accordingly.

---

## Go Standard Library

The project uses the official Go standard library (stdlib). No license required.

**Used for**: Core controller implementation, testing framework

---

## bbolt

A pure Go embedded key-value store.

**License**: MIT  
**Source**: [https://github.com/etcd-io/bbolt](https://github.com/etcd-io/bbolt)

**Used for**: Persistent operation state storage

---

## go-zero or Other HTTP Frameworks (if used)

*List any HTTP frameworks if added.*

---

## Testing Dependencies

### testify/assert or testify/mock

For testing and mocking.

**License**: MIT  
**Source**: [https://github.com/stretchr/testify](https://github.com/stretchr/testify)

**Used for**: Unit test assertions and mock objects

---

## Other Dependencies (as applicable)

Any additional third-party packages should be listed here with:

- Package name and version
- License type
- Source URL
- Purpose in the project

---

## Adding New Dependencies

When adding a new dependency:

1. Check license compatibility with MIT or similar permissive license
2. Include full license text in `THIRD_PARTY_NOTICES.md` in root repo
3. Update this wiki page to reflect changes
4. Document why the dependency is needed

---

## Updating Licenses

If dependencies are upgraded:

1. Check new license terms
2. Compare with original license requirements  
3. Update `THIRD_PARTY_NOTICES.md` accordingly
4. Note any significant differences in behavior or features

---

*Last updated: November 2026*