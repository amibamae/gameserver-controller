package coordination

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// lockPoll is how often a waiting command retries the lock file.
const lockPoll = 20 * time.Millisecond

// acquireLock creates path exclusively, waiting up to timeout while another
// command holds it. A lock left behind by a crashed command is never broken
// automatically: the command fails closed and names the holder instead.
func acquireLock(path string, timeout time.Duration, now time.Time) (func() error, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return writeLockOwner(f, path, now)
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			return nil, lockHeldError(path, timeout)
		}
		time.Sleep(lockPoll)
	}
}

func writeLockOwner(f *os.File, path string, now time.Time) (func() error, error) {
	_, werr := fmt.Fprintf(f, "pid=%d acquired=%s\n", os.Getpid(), formatTime(now))
	cerr := f.Close()
	release := func() error {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove lock %s: %w", path, err)
		}
		return nil
	}
	if err := errors.Join(werr, cerr); err != nil {
		return nil, errors.Join(fmt.Errorf("write lock %s: %w", path, err), release())
	}
	return release, nil
}

func lockHeldError(path string, timeout time.Duration) error {
	holder := "holder unknown"
	if data, err := os.ReadFile(path); err == nil {
		holder = strings.TrimSpace(string(data))
	}
	return fmt.Errorf("coordination lock %s is still held after %s (%s); if no coordination command is running, remove the lock file and retry", path, timeout, holder)
}
