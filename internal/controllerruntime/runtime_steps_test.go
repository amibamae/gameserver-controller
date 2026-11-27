package controllerruntime

import (
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// runtimeR6StepName returns a build step's method name.
func runtimeR6StepName(step func(*runtimeBuild) error) string {
	name := goruntime.FuncForPC(reflect.ValueOf(step).Pointer()).Name()
	return name[strings.LastIndex(name, ".")+1:]
}

func TestRuntimeR6BuildStepsRunInCompositionOrder(t *testing.T) {
	var names []string
	for _, step := range runtimeBuildSteps {
		names = append(names, runtimeR6StepName(step))
	}
	const want = "loadSchemas,openStore,newRegistry,loadMonitorPolicy,registerAdapters,registerDefinitions,newEngine,recoverOperations,loadKeyring"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("build steps = %s, want %s", got, want)
	}
}

func TestRuntimeR6OperationAttemptsAddRetries(t *testing.T) {
	retries := 3
	for _, test := range []struct {
		defaults config.Defaults
		want     int
	}{
		{config.Defaults{}, 1},
		{config.Defaults{RetryLimit: &retries}, 4},
	} {
		if got := operationAttempts(test.defaults); got != test.want {
			t.Fatalf("operationAttempts(%v) = %d, want %d", test.defaults.RetryLimit, got, test.want)
		}
	}
}

func TestRuntimeR6CredentialsAreCreatedOnceAndNameTheAdapter(t *testing.T) {
	f := newFixture(t)
	credentials := &lazyCredentials{directory: f.credentials + "-absent"}
	if _, err := credentials.forAdapter("storage.first")(); err == nil ||
		err.Error() != "adapter storage.first needs systemd credentials: systemd credential directory is unavailable" {
		t.Fatalf("missing directory error = %v", err)
	}
	if credentials.resolver != nil {
		t.Fatal("a failed creation kept a resolver")
	}
	credentials.directory = f.credentials
	first, err := credentials.forAdapter("storage.first")()
	if err != nil {
		t.Fatal(err)
	}
	second, err := credentials.forAdapter("storage.second")()
	if err != nil || first != second {
		t.Fatalf("second adapter got %v (%v), want the shared resolver %v", second, err, first)
	}
}
