package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// helperEnv makes the test binary act as the coordination command, so the
// race test can run real, separate processes.
const helperEnv = "GSC_COORDINATION_HELPER_PROCESS"

func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("only runs as a helper process")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(run(args, os.Stdout, os.Stderr))
}

// TestConcurrentClaimAcrossProcesses starts several processes that claim
// the same task at once against one directory. The lock file and the
// revision check must let exactly one of them win.
func TestConcurrentClaimAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts subprocesses")
	}
	const n = 6
	dir := stateWithAgents(t, n)
	runs := claimInProcesses(dir, n)
	wins := 0
	for i, r := range runs {
		switch r.code {
		case 0:
			wins++
		case 1:
		default:
			t.Errorf("process %d exited %d:\n%s", i, r.code, r.output)
		}
	}
	if wins != 1 {
		t.Fatalf("%d processes claimed T1, want exactly 1: %+v", wins, runs)
	}
	if rev := registryRevision(t, dir); rev != 1 {
		t.Fatalf("revision = %d; want one committed claim", rev)
	}
}

// stateWithAgents writes the clean state with agents p0 to p<n-1>.
func stateWithAgents(t *testing.T, n int) string {
	t.Helper()
	agents := make([]string, 0, n)
	for i := 0; i < n; i++ {
		agents = append(agents, fmt.Sprintf(`{"id": "p%d", "name": "p", "role": "r", "capabilities": [], "status": "available", "lastHeartbeat": null, "currentTask": null, "worktree": null}`, i))
	}
	files := maps.Clone(cleanState)
	files[".agents.json"] = `{"version": "1.0", "agents": [` + strings.Join(agents, ",") + "]}"
	return writeState(t, files)
}

type processRun struct {
	code   int
	output string
}

// claimInProcesses runs "claim --task-id T1" as agent p<i> in n processes
// at once.
func claimInProcesses(dir string, n int) []processRun {
	runs := make([]processRun, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--",
			"claim", "--dir", dir, "--agent", fmt.Sprintf("p%d", i), "--task-id", "T1")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := cmd.CombinedOutput()
			runs[i] = processRun{exitCode(err), string(out)}
		}()
	}
	wg.Wait()
	return runs
}

// exitCode maps a finished command's error to its exit code, or -1 when
// the process could not run.
func exitCode(err error) int {
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.ExitCode()
	default:
		return -1
	}
}

func registryRevision(t *testing.T, dir string) int64 {
	t.Helper()
	var reg struct {
		Revision int64 `json:"revision"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	return reg.Revision
}
