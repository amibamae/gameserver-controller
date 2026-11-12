//! Fails if a crate in Cargo.lock is not credited in THIRD_PARTY_NOTICES.md,
//! as the repository's CLAUDE.md requires for all third-party software.

#[test]
fn every_locked_crate_is_credited() {
    let manifest_dir = env!("CARGO_MANIFEST_DIR");
    let lock =
        std::fs::read_to_string(format!("{manifest_dir}/Cargo.lock")).expect("read Cargo.lock");
    let notices = std::fs::read_to_string(format!("{manifest_dir}/../THIRD_PARTY_NOTICES.md"))
        .expect("read THIRD_PARTY_NOTICES.md");
    let mut missing = Vec::new();
    let (mut name, mut version) = (None::<String>, None::<String>);
    for line in lock.lines().chain(std::iter::once("[[package]]")) {
        if line == "[[package]]" {
            if let (Some(n), Some(v)) = (name.take(), version.take()) {
                if n != env!("CARGO_PKG_NAME") && !notices.contains(&format!("| `{n}` | {v} |")) {
                    missing.push(format!("{n} {v}"));
                }
            }
        } else if let Some(value) = line.strip_prefix("name = ") {
            name = Some(value.trim_matches('"').to_string());
        } else if let Some(value) = line.strip_prefix("version = ") {
            version = Some(value.trim_matches('"').to_string());
        }
    }
    assert!(
        missing.is_empty(),
        "crates missing from THIRD_PARTY_NOTICES.md: {missing:?}"
    );
}
