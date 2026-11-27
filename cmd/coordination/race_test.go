package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	agents := `{"version": "1.0", "agents": [`
	for i := 0; i < n; i++ {
		if i > 0 {
			agents += ","
		}
		agents += fmt.Sprintf(`{"id": "p%d", "name": "p", "role": "r", "capabilities": [], "status": "available", "lastHeartbeat": null, "currentTask": null, "worktree": null}`, i)
	}
	files := map[string]string{}
	for k, v := range cleanState {
		files[k] = v
	}
	files[".agents.json"] = agents + "]}"
	dir := writeState(t, files)

	codes := make([]int, n)
	outputs := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--",
			"claim", "--dir", dir, "--agent", fmt.Sprintf("p%d", i), "--task-id", "T1")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := cmd.CombinedOutput()
			outputs[i] = out
			var exit *exec.ExitError
			switch {
			case err == nil:
				codes[i] = 0
			case errors.As(err, &exit):
				codes[i] = exit.ExitCode()
			default:
				codes[i] = -1
			}
		}()
	}
	wg.Wait()

	wins := 0
	for i, code := range codes {
		switch code {
		case 0:
			wins++
		case 1:
		default:
			t.Errorf("process %d exited %d:\n%s", i, code, outputs[i])
		}
	}
	if wins != 1 {
		t.Fatalf("%d processes claimed T1, want exactly 1; exit codes %v", wins, codes)
	}
	var reg struct {
		Revision int64 `json:"revision"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &reg); err != nil || reg.Revision != 1 {
		t.Fatalf("revision = %d, %v; want one committed claim", reg.Revision, err)
	}
}
