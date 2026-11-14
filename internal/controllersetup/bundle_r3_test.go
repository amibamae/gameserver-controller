package controllersetup

import (
	"fmt"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// These characterization tests pin what BuildInstallBundle renders and the
// exact reason each layout is refused. They were written against the code
// before the complexity refactor and must keep passing unchanged.

const setupR3TestUnit = `[Unit]
Description=Game Server Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=gameserver-controller
Group=gameserver-controller
ExecStart=/tmp/controller-setup-test/controller serve --config /tmp/controller-setup-test/controller.yaml
Restart=on-failure
RestartSec=5s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
ReadWritePaths=/tmp/controller-setup-test/state
LoadCredentialEncrypted=controller-idempotency-key-v1:/tmp/controller-setup-test/credentials/controller-idempotency-key-v1
LoadCredentialEncrypted=example-vultr-api-token:/tmp/controller-setup-test/credentials/example-vultr-api-token
LoadCredentialEncrypted=example-wasabi-staging-writer:/tmp/controller-setup-test/credentials/example-wasabi-staging-writer

[Install]
WantedBy=multi-user.target
`

const setupR3DefaultUnit = `[Unit]
Description=Game Server Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=gameserver-controller
Group=gameserver-controller
ExecStart=/usr/local/bin/controller serve --config /etc/gameserver-controller/controller.yaml
Restart=on-failure
RestartSec=5s
UMask=0077
StateDirectory=gameserver-controller
StateDirectoryMode=0750
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
ReadWritePaths=/var/lib/gameserver-controller
LoadCredentialEncrypted=controller-idempotency-key-v1:/etc/credstore.encrypted/controller-idempotency-key-v1
LoadCredentialEncrypted=example-vultr-api-token:/etc/credstore.encrypted/example-vultr-api-token
LoadCredentialEncrypted=example-wasabi-staging-writer:/etc/credstore.encrypted/example-wasabi-staging-writer

[Install]
WantedBy=multi-user.target
`

// setupR3FileLines summarizes files as "path mode owner:group content".
func setupR3FileLines(files []InstallFile) []string {
	lines := make([]string, 0, len(files))
	for _, file := range files {
		lines = append(lines, fmt.Sprintf("%s %v %s:%s %q", file.Path, file.Mode, file.Owner, file.Group, file.Content))
	}
	return lines
}

func setupR3YAML(t *testing.T, value any) []byte {
	t.Helper()
	content, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// setupR3MainConfig is the configuration the bundle should render: the
// planned state path, without the adapters loaded from adapters.d.
func setupR3MainConfig(t *testing.T, value config.Config, layout Layout) []byte {
	t.Helper()
	main := value
	main.Controller.StateStore.Path = layout.StateDatabaseFile
	main.Adapters = nil
	for _, profile := range value.Adapters {
		if profile.Source == "" {
			main.Adapters = append(main.Adapters, profile)
		}
	}
	return setupR3YAML(t, main)
}

func setupR3AssertDeepEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %+v\nwant %+v", what, got, want)
	}
}

func TestSetupR3BuildInstallBundleExactContents(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	binary := []byte("test-controller-binary")
	bundle, err := BuildInstallBundle(value, layout, binary)
	if err != nil {
		t.Fatal(err)
	}
	binary[0] = 'X' // the bundle keeps its own copy of the executable
	setupR3AssertDeepEqual(t, "directories", bundle.Directories, []InstallDirectory{
		{Path: "/tmp/controller-setup-test", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/state", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
	})
	setupR3AssertDeepEqual(t, "state directory", bundle.StateDir, "/tmp/controller-setup-test/state")
	setupR3AssertDeepEqual(t, "files", setupR3FileLines(bundle.Files), setupR3FileLines([]InstallFile{
		{Path: "/tmp/controller-setup-test/controller", Mode: 0o755, Owner: "root", Group: "root", Content: []byte("test-controller-binary")},
		{Path: "/tmp/controller-setup-test/controller.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: setupR3MainConfig(t, value, layout)},
		{Path: "/tmp/controller-setup-test/service.unit", Mode: 0o644, Owner: "root", Group: "root", Content: []byte(setupR3TestUnit)},
	}))
}

func TestSetupR3BuildInstallBundleDefaultLayoutUnit(t *testing.T) {
	value := loadExampleConfig(t)
	plan, err := BuildPlan(value, debianHost, Layout{
		ConfigFile:    "/etc/gameserver-controller/controller.yaml",
		ServiceFile:   "/etc/systemd/system/gameserver-controller.service",
		BinaryFile:    "/usr/local/bin/controller",
		StateDir:      "/var/lib/gameserver-controller",
		CredentialDir: "/etc/credstore.encrypted",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildInstallBundle(value, plan.Layout, []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	// Files are sorted by path: the configuration, the unit, the controller.
	unit := bundle.Files[1]
	setupR3AssertDeepEqual(t, "unit path", unit.Path, "/etc/systemd/system/gameserver-controller.service")
	setupR3AssertDeepEqual(t, "unit", string(unit.Content), setupR3DefaultUnit)
}

func TestSetupR3BuildInstallBundleAdapterFilesAndDirectories(t *testing.T) {
	value := loadExampleConfig(t)
	var moved config.AdapterProfile
	for i := range value.Adapters {
		if value.Adapters[i].ID == "game.paper" {
			value.Adapters[i].Source = "30-paper.yaml"
			moved = value.Adapters[i]
		}
	}
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	setupR3AssertDeepEqual(t, "directories", bundle.Directories, []InstallDirectory{
		{Path: "/tmp/controller-setup-test", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/adapters.d", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/state", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
	})
	setupR3AssertDeepEqual(t, "files", setupR3FileLines(bundle.Files), setupR3FileLines([]InstallFile{
		{Path: "/tmp/controller-setup-test/adapters.d/30-paper.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: setupR3YAML(t, moved)},
		{Path: "/tmp/controller-setup-test/controller", Mode: 0o755, Owner: "root", Group: "root", Content: []byte("binary")},
		{Path: "/tmp/controller-setup-test/controller.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: setupR3MainConfig(t, value, layout)},
		{Path: "/tmp/controller-setup-test/service.unit", Mode: 0o644, Owner: "root", Group: "root", Content: bundle.Files[3].Content},
	}))
}

type setupR3LayoutCase struct {
	name   string
	layout func(*Layout)
	want   string
}

var setupR3LayoutCases = []setupR3LayoutCase{
	{name: "relative configuration", layout: func(l *Layout) { l.ConfigFile = "controller.yaml" }, want: "configuration destination must be a clean absolute path"},
	{name: "unclean unit", layout: func(l *Layout) { l.ServiceFile = "/tmp/x/../service.unit" }, want: "systemd unit destination must be a clean absolute path"},
	{name: "NUL in controller", layout: func(l *Layout) { l.BinaryFile = "/tmp/contr\x00oller" }, want: "controller destination must be a clean absolute path"},
	{name: "empty state", layout: func(l *Layout) { l.StateDir = "" }, want: "state destination must be a clean absolute path"},
	{name: "trailing slash credentials", layout: func(l *Layout) { l.CredentialDir = "/tmp/credentials/" }, want: "credentials destination must be a clean absolute path"},
	{name: "space in configuration", layout: func(l *Layout) { l.ConfigFile = "/tmp/a b/controller.yaml" }, want: "configuration destination contains unsupported characters"},
	{name: "percent in unit", layout: func(l *Layout) { l.ServiceFile = "/tmp/%i.service" }, want: "systemd unit destination contains unsupported characters"},
	{name: "dollar in controller", layout: func(l *Layout) { l.BinaryFile = "/tmp/$controller" }, want: "controller destination contains unsupported characters"},
	{name: "semicolon in state", layout: func(l *Layout) { l.StateDir = "/tmp/state;x" }, want: "state destination contains unsupported characters"},
	{name: "unicode in credentials", layout: func(l *Layout) { l.CredentialDir = "/tmp/cré" }, want: "credentials destination contains unsupported characters"},
	{name: "missing state database", layout: func(l *Layout) { l.StateDatabaseFile = "" }, want: "planned controller state database path is required"},
	{name: "relative state database", layout: func(l *Layout) { l.StateDatabaseFile = "state/controller.db" }, want: "state database destination must be a clean absolute path"},
	{name: "unclean state database", layout: func(l *Layout) { l.StateDatabaseFile = l.StateDir + "/x/../controller.db" }, want: "state database destination must be a clean absolute path"},
	{name: "state database outside", layout: func(l *Layout) { l.StateDatabaseFile = "/tmp/controller.db" }, want: "state database must stay inside the private state directory"},
	{name: "state database is the state directory", layout: func(l *Layout) { l.StateDatabaseFile = l.StateDir }, want: "state database must stay inside the private state directory"},
	{name: "credentials inside state", layout: func(l *Layout) { l.CredentialDir = l.StateDir + "/credentials" }, want: "state and credential directories must not overlap"},
	{name: "state inside credentials", layout: func(l *Layout) {
		l.StateDir = l.CredentialDir + "/state"
		l.StateDatabaseFile = l.StateDir + "/controller.db"
	}, want: "state and credential directories must not overlap"},
	{name: "same state and credentials", layout: func(l *Layout) { l.CredentialDir = l.StateDir }, want: "state and credential directories must not overlap"},
	{name: "configuration inside state", layout: func(l *Layout) { l.ConfigFile = l.StateDir + "/controller.yaml" }, want: "installation files must stay outside state and credential directories"},
	{name: "unit inside credentials", layout: func(l *Layout) { l.ServiceFile = l.CredentialDir + "/service.unit" }, want: "installation files must stay outside state and credential directories"},
	{name: "controller is the state directory", layout: func(l *Layout) { l.BinaryFile = l.StateDir }, want: "installation files must stay outside state and credential directories"},
	{name: "unit is the controller", layout: func(l *Layout) { l.ServiceFile = l.BinaryFile }, want: "installation destinations overlap"},
	{name: "configuration is the unit", layout: func(l *Layout) { l.ServiceFile = l.ConfigFile }, want: "installation file destinations must be unique"},
	{name: "configuration is the controller", layout: func(l *Layout) { l.BinaryFile = l.ConfigFile }, want: "installation file destinations must be unique"},
}

// setupR3AssertBundleRefused checks the exact error and the empty bundle.
func setupR3AssertBundleRefused(t *testing.T, value config.Config, layout Layout, binary []byte, want string) {
	t.Helper()
	bundle, err := BuildInstallBundle(value, layout, binary)
	if err == nil || err.Error() != want {
		t.Fatalf("BuildInstallBundle = %v, want %q", err, want)
	}
	setupR3AssertDeepEqual(t, "refused bundle", bundle, InstallBundle{})
}

func TestSetupR3BuildInstallBundleLayoutErrors(t *testing.T) {
	value := loadExampleConfig(t)
	base := buildTestPlan(t, value).Layout
	for _, test := range setupR3LayoutCases {
		t.Run(test.name, func(t *testing.T) {
			layout := base
			test.layout(&layout)
			setupR3AssertBundleRefused(t, value, layout, []byte("binary"), test.want)
		})
	}
}

func TestSetupR3BuildInstallBundleInputErrors(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	setupR3AssertBundleRefused(t, config.Config{}, layout, []byte("binary"), "controller configuration is invalid")
	setupR3AssertBundleRefused(t, value, layout, []byte{}, "controller executable is empty")
	setupR3AssertBundleRefused(t, value, layout, nil, "controller executable is empty")
	// The configuration is checked before the executable and the layout.
	setupR3AssertBundleRefused(t, config.Config{}, Layout{}, nil, "controller configuration is invalid")
	setupR3AssertBundleRefused(t, value, Layout{}, nil, "controller executable is empty")
}
