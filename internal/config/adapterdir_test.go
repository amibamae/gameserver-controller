package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleSettings is a test-only implementation that exercises the generic
// implementation machinery without depending on a real adapter.
type exampleSettings struct {
	Mode          string `yaml:"mode"`
	EnvironmentID string `yaml:"environment_id"`
}

func (e *exampleSettings) Validate(check SettingsCheck) {
	check.Require(e.Mode == "ok", "mode must be ok")
}

func (e *exampleSettings) BoundEnvironment() string { return e.EnvironmentID }

func init() {
	RegisterImplementation(Implementation{Name: "example", Category: "game", Secrets: []string{},
		NewSettings: func() ImplementationSettings { return &exampleSettings{} }})
}

const exampleProfile = `  - id: game.paper
    category: game
    implementation: example
    example:
      mode: ok
`

func withExample(profile string) string {
	return strings.Replace(validConfig, `  - id: game.paper
    category: game
    secret_refs:
      server_password:
        secret_ref: systemd-credential://example-game-server-password
        purpose: test-only example
        permission_scope: [game.server.read]
`, profile, 1)
}

func TestImplementationSettingsAreDecodedAndValidated(t *testing.T) {
	input := withExample(exampleProfile)
	if input == validConfig {
		t.Fatal("fixture replacement did not apply")
	}
	value, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range value.Adapters {
		if profile.ID == "game.paper" {
			settings, ok := SettingsOf[*exampleSettings](profile)
			if !ok || settings.Mode != "ok" {
				t.Fatalf("settings not decoded: %+v", profile.Settings)
			}
			return
		}
	}
	t.Fatal("profile missing")
}

func TestImplementationProfileProblems(t *testing.T) {
	tests := map[string]struct{ before, after, want string }{
		"unknown block key":          {"    example:\n", "    exampel:\n", "has unknown field exampel"},
		"block without impl":         {"    implementation: example\n", "", "has example settings but no implementation"},
		"missing block":              {"    example:\n      mode: ok\n", "", "example is required with implementation example"},
		"unknown field in block":     {"      mode: ok\n", "      mode: ok\n      extra: 1\n", "example has an unknown field"},
		"setting fails validation":   {"      mode: ok\n", "      mode: bad\n", "example.mode must be ok"},
		"wrong category":             {"    category: game\n    implementation", "    category: hosting\n    implementation", "requires category game"},
		"wrong secrets":              {"    implementation: example\n", "    implementation: example\n    secret_refs:\n      token:\n        secret_ref: systemd-credential://x\n        purpose: test\n", "secret_refs must define exactly"},
		"unregistered impl":          {"    implementation: example\n", "    implementation: missing\n", "implementation must be one of"},
		"bound to other environment": {"      mode: ok\n", "      mode: ok\n      environment_id: production\n", "must use an adapter bound to the same environment"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			base := withExample(exampleProfile)
			input := strings.Replace(base, test.before, test.after, 1)
			if input == base {
				t.Fatalf("replacement %q did not apply", test.before)
			}
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want %q", err, test.want)
			}
		})
	}
}

// writeConfigDir writes validConfig without game.paper, plus adapters.d files.
func writeConfigDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "controller.yaml")
	if err := os.WriteFile(path, []byte(withExample("")), 0o600); err != nil {
		t.Fatal(err)
	}
	if files != nil {
		if err := os.Mkdir(filepath.Join(dir, AdapterDirectoryName), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, AdapterDirectoryName, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const paperFile = `id: game.paper
category: game
implementation: example
example:
  mode: ok
`

func TestLoadAddsOneAdapterPerFile(t *testing.T) {
	path := writeConfigDir(t, map[string]string{"20-paper.yaml": paperFile, "README.txt": "ignored", "paper.yaml~": "ignored"})
	value, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	last := value.Adapters[len(value.Adapters)-1]
	if last.ID != "game.paper" || last.Source != "20-paper.yaml" || last.Settings == nil {
		t.Fatalf("adapter from file = %+v", last)
	}

	// Removing the file removes the adapter, which game.paper's servers need.
	if err := os.Remove(filepath.Join(filepath.Dir(path), AdapterDirectoryName, "20-paper.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "references unknown adapter game.paper") {
		t.Fatalf("Load after removing the file = %v", err)
	}
}

func TestLoadRejectsUnsafeAdapterFiles(t *testing.T) {
	t.Run("duplicate id", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"a.yaml": paperFile, "b.yaml": paperFile})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate adapter ID: game.paper") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("two documents", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"a.yaml": paperFile + "---\n" + paperFile})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "exactly one adapter profile") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("bad file name", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"Paper.yaml": paperFile})
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "file names must be") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("group writable", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"a.yaml": paperFile})
		if err := os.Chmod(filepath.Join(filepath.Dir(path), AdapterDirectoryName, "a.yaml"), 0o660); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "writable by group or others") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"real.txt": paperFile})
		dir := filepath.Join(filepath.Dir(path), AdapterDirectoryName)
		if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "a.yaml")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("Load = %v", err)
		}
	})
	t.Run("unknown field does not echo values", func(t *testing.T) {
		path := writeConfigDir(t, map[string]string{"a.yaml": paperFile + "secret_value: hunter2\n"})
		_, err := Load(path)
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("Load = %v", err)
		}
	})
}
