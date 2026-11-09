package controllersetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

func TestBuildPlanReportsSupportedExistingMachineWithoutSecrets(t *testing.T) {
	value := loadExample(t)
	secretRef := value.Adapters[0].SecretRefs["api_token"]
	secretRef.SecretRef = "systemd-credential://private-vultr-reference"
	value.Adapters[0].SecretRefs["api_token"] = secretRef
	plan, err := BuildPlan(value, Host{OSReleaseID: "debian", Systemd: true}, testLayout())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.TargetSupported || plan.Target != "existing-machine" {
		t.Fatalf("unexpected target status: %+v", plan)
	}
	if plan.CredentialRefCount != 2 {
		t.Fatalf("credential references = %d, want 2", plan.CredentialRefCount)
	}
	serialized := strings.Join(plan.Actions, " ") + plan.Layout.ConfigFile
	if strings.Contains(serialized, "private-vultr-reference") {
		t.Fatalf("plan exposed a credential reference: %q", serialized)
	}
}

func TestBuildPlanReportsUnsupportedHostAsBlocker(t *testing.T) {
	plan, err := BuildPlan(loadExample(t), Host{OSReleaseID: "ubuntu"}, testLayout())
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetSupported || len(plan.Blockers) != 2 {
		t.Fatalf("unexpected unsupported-host plan: %+v", plan)
	}
}

func TestBuildPlanRequiresAbsoluteDestinations(t *testing.T) {
	layout := testLayout()
	layout.StateDir = "relative-state"
	if _, err := BuildPlan(loadExample(t), Host{OSReleaseID: "debian", Systemd: true}, layout); err == nil {
		t.Fatal("BuildPlan accepted relative state path")
	}
}

func TestBuildPlanRejectsStateDatabaseOutsidePrivateDirectory(t *testing.T) {
	value := loadExample(t)
	value.Controller.StateStore.Path = "../../outside/controller.db"
	if _, err := BuildPlan(value, Host{OSReleaseID: "debian", Systemd: true}, testLayout()); err == nil {
		t.Fatal("BuildPlan accepted a state database outside the private state directory")
	}
}

func loadExample(t *testing.T) config.Config {
	t.Helper()
	value, err := config.Load(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testLayout() Layout {
	root := filepath.Join(string(os.PathSeparator), "tmp", "controller-setup-test")
	return Layout{
		ConfigFile:    filepath.Join(root, "controller.yaml"),
		ServiceFile:   filepath.Join(root, "service.unit"),
		BinaryFile:    filepath.Join(root, "controller"),
		StateDir:      filepath.Join(root, "state"),
		CredentialDir: filepath.Join(root, "credentials"),
	}
}
