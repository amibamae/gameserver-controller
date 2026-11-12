package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AdapterDirectoryName is the directory, next to the configuration file, that
// holds one adapter profile per file. Adding a file adds an adapter; removing
// it removes the adapter.
const AdapterDirectoryName = "adapters.d"

const maxAdapterFileBytes = 1 << 20

var adapterFileName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}\.ya?ml$`)

// loadAdapterDirectory reads every *.yaml and *.yml file in dir, in name
// order. Each must be a regular file (not a symlink), at most 1 MiB, not
// writable by group or others, and contain exactly one adapter profile. Other
// files, such as editor backups, are ignored. A missing directory is not an
// error.
func loadAdapterDirectory(dir string) ([]AdapterProfile, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", AdapterDirectoryName, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s must be a directory, not a symlink or file", AdapterDirectoryName)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", AdapterDirectoryName, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		if !adapterFileName.MatchString(name) {
			return nil, fmt.Errorf("%s/%s: file names must be lower-case letters, digits, dots, dashes, or underscores", AdapterDirectoryName, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	profiles := make([]AdapterProfile, 0, len(names))
	for _, name := range names {
		profile, err := readAdapterFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", AdapterDirectoryName, name, err)
		}
		profile.Source = name
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func readAdapterFile(path string) (AdapterProfile, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return AdapterProfile{}, errors.New("cannot be read")
	}
	if !info.Mode().IsRegular() {
		return AdapterProfile{}, errors.New("must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return AdapterProfile{}, errors.New("must not be writable by group or others")
	}
	if info.Size() > maxAdapterFileBytes {
		return AdapterProfile{}, errors.New("is larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return AdapterProfile{}, errors.New("cannot be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAdapterFileBytes+1))
	if err != nil || len(data) > maxAdapterFileBytes {
		return AdapterProfile{}, errors.New("cannot be read")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var profile AdapterProfile
	if err := decoder.Decode(&profile); err != nil {
		// Do not echo the offending value; it could be a credential.
		return AdapterProfile{}, errors.New("decode YAML failed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return AdapterProfile{}, errors.New("must contain exactly one adapter profile document")
	}
	return profile, nil
}
