//! Package identity: recomputes the `sha256-path-filehash-v1` digests as
//! documented in protocol/conformance/requirements.md.

use sha2::{Digest, Sha256};
use std::fs;
use std::path::Path;

use crate::json::{self, Json};

pub const ALGORITHM: &str = "sha256-path-filehash-v1";

#[derive(Debug)]
pub struct Identity {
    pub protocol_version: String,
    pub schema_registry_digest: String,
    pub fixture_suite_digest: String,
    pub schema_count: usize,
    pub fixture_count: usize,
    /// Whether the recomputed digests equal the committed manifest.
    pub matches_manifest: bool,
}

/// Sorts repository-relative paths, hashes each file, then hashes the records
/// `path NUL lowercase-hex-file-digest LF`.
pub fn digest_files(root: &Path, paths: &[String]) -> std::io::Result<String> {
    let mut ordered = paths.to_vec();
    ordered.sort();
    let mut total = Sha256::new();
    for relative in &ordered {
        let content = fs::read(root.join(relative))?;
        let file_digest = hex(&Sha256::digest(&content));
        total.update(relative.as_bytes());
        total.update([0u8]);
        total.update(file_digest.as_bytes());
        total.update(b"\n");
    }
    Ok(format!("sha256:{}", hex(&total.finalize())))
}

pub fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub fn read_json(root: &Path, relative: &str) -> Result<Json, String> {
    let bytes = fs::read(root.join(relative)).map_err(|err| format!("read {relative}: {err}"))?;
    json::parse(&bytes).map_err(|err| format!("{relative}: {err}"))
}

pub fn fixture_paths(root: &Path) -> Result<Vec<String>, String> {
    let dir = root.join("protocol/conformance/test-vectors");
    let mut paths = Vec::new();
    for entry in fs::read_dir(&dir).map_err(|err| format!("read fixtures: {err}"))? {
        let name = entry
            .map_err(|err| err.to_string())?
            .file_name()
            .to_string_lossy()
            .into_owned();
        if name.ends_with(".json") {
            paths.push(format!("protocol/conformance/test-vectors/{name}"));
        }
    }
    paths.sort();
    Ok(paths)
}

pub fn identity(root: &Path) -> Result<Identity, String> {
    let index = read_json(root, "protocol/schemas/index.json")?;
    let mut schema_paths = vec!["protocol/schemas/index.json".to_string()];
    for entry in index.items("schemas") {
        let path = entry
            .str("path")
            .ok_or("schema index entry without a path")?;
        schema_paths.push(format!("protocol/schemas/{path}"));
    }
    let fixtures = fixture_paths(root)?;
    let schema_digest = digest_files(root, &schema_paths).map_err(|err| err.to_string())?;
    let fixture_digest = digest_files(root, &fixtures).map_err(|err| err.to_string())?;
    let manifest = read_json(root, "protocol/conformance/package-manifest.json")?;
    let matches = manifest.str("digest_algorithm") == Some(ALGORITHM)
        && manifest.str("schema_registry_digest") == Some(schema_digest.as_str())
        && manifest.str("fixture_suite_digest") == Some(fixture_digest.as_str());
    Ok(Identity {
        protocol_version: index
            .str("protocol_version")
            .unwrap_or_default()
            .to_string(),
        schema_registry_digest: schema_digest,
        fixture_suite_digest: fixture_digest,
        schema_count: schema_paths.len() - 1,
        fixture_count: fixtures.len(),
        matches_manifest: matches,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn digest_record_format_is_path_nul_hex_lf() {
        let dir =
            std::env::temp_dir().join(format!("conformance-rs-digest-{}", std::process::id()));
        fs::create_dir_all(dir.join("a")).unwrap();
        fs::write(dir.join("a/x.json"), b"{}").unwrap();
        let file = hex(&Sha256::digest(b"{}"));
        let expected = hex(&Sha256::digest(format!("a/x.json\0{file}\n").as_bytes()));
        assert_eq!(
            digest_files(&dir, &["a/x.json".into()]).unwrap(),
            format!("sha256:{expected}")
        );
        fs::remove_dir_all(dir).unwrap();
    }
}
