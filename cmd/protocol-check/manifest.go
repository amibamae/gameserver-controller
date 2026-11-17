package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
)

const packageDigestAlgorithm = "sha256-path-filehash-v1"

type conformancePackageManifest struct {
	ManifestVersion      int    `json:"manifest_version"`
	ProtocolVersion      string `json:"protocol_version"`
	DigestAlgorithm      string `json:"digest_algorithm"`
	SchemaRegistryDigest string `json:"schema_registry_digest"`
	FixtureSuiteDigest   string `json:"fixture_suite_digest"`
	SchemaCount          int    `json:"schema_count"`
	FixtureCount         int    `json:"fixture_count"`
}

func (pkg *protocolPackage) packageManifest() (conformancePackageManifest, error) {
	schemaPaths := []string{"protocol/schemas/index.json"}
	for _, entry := range pkg.entries {
		schemaPaths = append(schemaPaths, filepath.ToSlash(filepath.Join("protocol", "schemas", filepath.FromSlash(entry.Path))))
	}
	schemaDigest, err := digestPackageFiles(pkg.root, schemaPaths)
	if err != nil {
		return conformancePackageManifest{}, fmt.Errorf("digest schema registry: %w", err)
	}

	fixturePaths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return conformancePackageManifest{}, fmt.Errorf("discover conformance fixtures: %w", err)
	}
	relativeFixturePaths := make([]string, 0, len(fixturePaths))
	for _, path := range fixturePaths {
		relative, err := filepath.Rel(pkg.root, path)
		if err != nil {
			return conformancePackageManifest{}, fmt.Errorf("make fixture path relative: %w", err)
		}
		relativeFixturePaths = append(relativeFixturePaths, filepath.ToSlash(relative))
	}
	fixtureDigest, err := digestPackageFiles(pkg.root, relativeFixturePaths)
	if err != nil {
		return conformancePackageManifest{}, fmt.Errorf("digest conformance fixtures: %w", err)
	}

	return conformancePackageManifest{
		ManifestVersion:      1,
		ProtocolVersion:      pkg.version,
		DigestAlgorithm:      packageDigestAlgorithm,
		SchemaRegistryDigest: schemaDigest,
		FixtureSuiteDigest:   fixtureDigest,
		SchemaCount:          len(pkg.entries),
		FixtureCount:         len(relativeFixturePaths),
	}, nil
}

func digestPackageFiles(root string, paths []string) (string, error) {
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	digest := sha256.New()
	for _, relative := range ordered {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", relative, err)
		}
		fileDigest := sha256.Sum256(content)
		if err := writeDigestRecord(digest, relative, hex.EncodeToString(fileDigest[:])); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeDigestRecord(digest hash.Hash, path, fileDigest string) error {
	for _, part := range [][]byte{[]byte(path), {0}, []byte(fileDigest), {'\n'}} {
		if _, err := digest.Write(part); err != nil {
			return fmt.Errorf("write package digest: %w", err)
		}
	}
	return nil
}
