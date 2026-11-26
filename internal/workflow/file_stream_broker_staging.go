package workflow

// Staging directory upkeep: the usage and budget check over every staged
// file, the expiry sweep, and clearing earlier attempts of a retried step.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// stagingUsageLocked totals the staged bytes under the root, plus the unused
// maximum of every incomplete sink, and checks each staged file against its
// record. It fails on the first file, in walk order, that breaks a check.
func (b *FileServerBackupTransferBroker) stagingUsageLocked() (int64, error) {
	tally := &stagingTally{budget: b.maxStagingBytes}
	if err := filepath.WalkDir(b.root, tally.visit); err != nil {
		return 0, err
	}
	return tally.used, nil
}

// stagingTally accumulates staging usage against a byte budget.
type stagingTally struct {
	budget int64
	used   int64
}

// visit is the WalkDir callback that counts one staged byte file.
func (t *stagingTally) visit(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() || filepath.Ext(path) != ".bin" {
		return nil
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("invalid file in stream staging directory")
	}
	return t.countStagedFile(path, info.Size())
}

// countStagedFile adds a staged file's size, checks its record, then adds
// the reservation an incomplete sink still holds.
func (t *stagingTally) countStagedFile(path string, size int64) error {
	if err := t.add(size, "existing backup staging data exceeds the configured budget"); err != nil {
		return err
	}
	record, err := readUsageRecord(path + ".json")
	if err != nil {
		return err
	}
	if err := checkUsageRecord(record, size, t.budget); err != nil {
		return err
	}
	if err := t.reserve(record, size); err != nil {
		return err
	}
	if t.used > t.budget {
		return errors.New("existing backup staging data exceeds the configured budget")
	}
	return nil
}

// add counts size bytes, or returns message if they do not fit the budget.
func (t *stagingTally) add(size int64, message string) error {
	if t.used > t.budget || size > t.budget-t.used {
		return errors.New(message)
	}
	t.used += size
	return nil
}

// reserve counts the bytes an incomplete sink may still write.
func (t *stagingTally) reserve(record stagedStreamRecord, size int64) error {
	if !record.StoresBytes || record.Complete {
		return nil
	}
	reserved := record.MaximumBytes - size
	if reserved <= 0 {
		return nil
	}
	return t.add(reserved, "existing backup staging reservation exceeds the configured budget")
}

// readUsageRecord reads the transfer record beside a staged byte file. Unlike
// readStagedStreamRecord it follows symlinks, as the usage check always has.
func readUsageRecord(path string) (stagedStreamRecord, error) {
	var record stagedStreamRecord
	encoded, err := os.ReadFile(path)
	if err != nil {
		return record, errors.New("staged byte file has no matching transfer record")
	}
	if json.Unmarshal(encoded, &record) != nil {
		return record, errors.New("staged byte file has an invalid transfer record")
	}
	return record, nil
}

// checkUsageRecord checks a staged file's record: identity and bounds first,
// then, for a completed record, its measurements.
func checkUsageRecord(record stagedStreamRecord, size, budget int64) error {
	if !usageRecordValid(record, size, budget) {
		return errors.New("staged byte file has invalid size bounds or identity metadata")
	}
	if record.Complete && !usageRecordMeasured(record, size) {
		return errors.New("completed staged bytes do not match their transfer record")
	}
	return nil
}

// usageRecordValid reports whether a record stores bytes, names its stream,
// and has a maximum that holds the file and fits the budget.
func usageRecordValid(record stagedStreamRecord, size, budget int64) bool {
	return record.StoresBytes && usageRecordIdentified(record) &&
		record.MaximumBytes >= size && withinBudget(record.MaximumBytes, budget)
}

func usageRecordIdentified(record stagedStreamRecord) bool {
	return recordNamesStream(record) && !record.ExpiresAt.IsZero() && isSHA256(record.InputSHA256)
}

func recordNamesStream(record stagedStreamRecord) bool {
	return record.OperationID != "" && record.StepID != "" && record.Attempt >= 1 && record.DataSetID != ""
}

func withinBudget(maximumBytes, budget int64) bool {
	return maximumBytes >= 1 && maximumBytes <= budget
}

// usageRecordMeasured reports whether a completed record measured exactly
// the staged file, within its maximum, with a SHA-256 digest.
func usageRecordMeasured(record stagedStreamRecord, size int64) bool {
	return record.ActualSize == size && record.ActualSize <= record.MaximumBytes && isSHA256(record.ActualSHA256)
}

func (b *FileServerBackupTransferBroker) sweepExpiredLocked(now time.Time) (int, error) {
	entries, err := os.ReadDir(b.root)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !isSafeStreamKey(entry.Name()) {
			continue
		}
		path := filepath.Join(b.root, entry.Name())
		touched, err := directoryLatestModTime(path)
		if err != nil {
			return removed, err
		}
		if !touched.IsZero() && !touched.Add(b.artifactLifetime).After(now) {
			if err := os.RemoveAll(path); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func directoryLatestModTime(root string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest, err
}

func (b *FileServerBackupTransferBroker) clearPriorStepAttempts(operationID, stepID string, currentAttempt int) error {
	stepDirectory := filepath.Join(b.operationDirectory(operationID), safeStreamKey(stepID))
	entries, err := os.ReadDir(stepDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !priorAttemptEntry(entry, currentAttempt) {
			continue
		}
		if err := removePriorAttempt(filepath.Join(stepDirectory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// priorAttemptEntry reports whether entry is an attempt-N directory with
// 1 <= N < currentAttempt.
func priorAttemptEntry(entry os.DirEntry, currentAttempt int) bool {
	if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "attempt-") {
		return false
	}
	attempt, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "attempt-"))
	return err == nil && attempt >= 1 && attempt < currentAttempt
}

// removePriorAttempt removes an earlier attempt directory after checking,
// without following links, that it is still a real directory.
func removePriorAttempt(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prior stream attempt path is not a real directory")
	}
	return os.RemoveAll(path)
}
