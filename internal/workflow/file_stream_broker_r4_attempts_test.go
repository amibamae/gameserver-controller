package workflow

// Round-4 characterization of clearPriorStepAttempts: which entries of a
// step directory count as earlier attempts and are removed.

import (
	"os"
	"path/filepath"
	"testing"
)

func brokerR4StepDir(broker *FileServerBackupTransferBroker) string {
	return filepath.Join(broker.operationDirectory("op"), safeStreamKey("step"))
}

func TestBrokerR4ClearPriorStepAttempts(t *testing.T) {
	t.Run("missing step directory", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		brokerR3WantResult(t, broker.clearPriorStepAttempts("op", "step", 3), "")
	})
	t.Run("bounds and names", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		stepDir := brokerR4StepDir(broker)
		for _, dir := range []string{"attempt-1", "attempt-2", "attempt-3", "attempt--1", "attempt-+2", "attempt-", "Attempt-1"} {
			brokerR3Write(t, filepath.Join(stepDir, dir, "old.part"), []byte("old"))
		}
		brokerR3WantResult(t, broker.clearPriorStepAttempts("op", "step", 3), "")
		// strconv.Atoi accepts a leading plus sign, so attempt-+2 is attempt 2.
		for _, removed := range []string{"attempt-1", "attempt-2", "attempt-+2"} {
			brokerR3Exists(t, filepath.Join(stepDir, removed), false)
		}
		for _, kept := range []string{"attempt-3", "attempt--1", "attempt-", "Attempt-1"} {
			brokerR3Exists(t, filepath.Join(stepDir, kept), true)
		}
	})
	t.Run("first attempt clears nothing", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		stepDir := brokerR4StepDir(broker)
		brokerR3Write(t, filepath.Join(stepDir, "attempt-1", "old.part"), []byte("old"))
		brokerR3WantResult(t, broker.clearPriorStepAttempts("op", "step", 1), "")
		brokerR3Exists(t, filepath.Join(stepDir, "attempt-1"), true)
	})
	t.Run("symlinked attempt is skipped", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		stepDir := brokerR4StepDir(broker)
		target := filepath.Join(t.TempDir(), "elsewhere")
		brokerR3Write(t, filepath.Join(target, "keep"), []byte("keep"))
		if err := os.MkdirAll(stepDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(stepDir, "attempt-1")); err != nil {
			t.Fatal(err)
		}
		brokerR3WantResult(t, broker.clearPriorStepAttempts("op", "step", 2), "")
		brokerR3Exists(t, filepath.Join(stepDir, "attempt-1"), true)
		brokerR3WantContent(t, filepath.Join(target, "keep"), "keep")
	})
}

// TestBrokerR4RemovePriorAttemptGuards covers the checks that only a change
// between listing and removal can reach through clearPriorStepAttempts.
func TestBrokerR4RemovePriorAttemptGuards(t *testing.T) {
	const notReal = "prior stream attempt path is not a real directory"
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	_, wantMissing := os.Lstat(missing)
	brokerR3WantErr(t, removePriorAttempt(missing), wantMissing.Error())

	file := filepath.Join(dir, "file")
	brokerR3Write(t, file, []byte("file"))
	brokerR3WantErr(t, removePriorAttempt(file), notReal)
	brokerR3Exists(t, file, true)

	target := filepath.Join(dir, "target")
	brokerR3Write(t, filepath.Join(target, "keep"), []byte("keep"))
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	brokerR3WantErr(t, removePriorAttempt(link), notReal)
	brokerR3WantContent(t, filepath.Join(target, "keep"), "keep")

	brokerR3WantResult(t, removePriorAttempt(target), "")
	brokerR3Exists(t, target, false)
}
