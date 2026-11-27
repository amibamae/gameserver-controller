package coordination

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// AnyRevision tells Apply to accept whatever revision it reads. Apply then
// retries a commit that lost a race, re-running the mutation on the newer
// state, so its checks (such as "already claimed") see the winner's write.
const AnyRevision int64 = -1

// Default store settings.
const (
	DefaultLockTimeout = 10 * time.Second
	DefaultMaxAttempts = 5
)

// ErrStale matches every *StaleError.
var ErrStale = errors.New("stale update")

// StaleError rejects a commit whose snapshot is older than the state on
// disk.
type StaleError struct {
	Current  int64
	Expected int64
}

func (e *StaleError) Error() string {
	return fmt.Sprintf("stale update: coordination state is at revision %d, expected %d; reload state and retry", e.Current, e.Expected)
}

// Is reports whether target is ErrStale.
func (e *StaleError) Is(target error) bool { return target == ErrStale }

// Store reads and commits the state in one coordination directory.
type Store struct {
	Dir         string
	Now         func() time.Time
	LockTimeout time.Duration
	MaxAttempts int

	// beforeCommit, when set, runs between applying a mutation and
	// committing it. Tests use it to force two writers onto one snapshot.
	beforeCommit func()
}

// NewStore returns a store for dir with the default settings.
func NewStore(dir string) *Store {
	return &Store{Dir: dir, Now: time.Now, LockTimeout: DefaultLockTimeout, MaxAttempts: DefaultMaxAttempts}
}

// Result is a finished Apply.
type Result struct {
	Outcome
	Revision  int64
	Committed bool
}

// Load reads one consistent snapshot under the lock. It fails closed: any
// unreadable, unknown or inconsistent file is an error.
func (s *Store) Load() (*State, error) {
	release, err := s.lock(s.Now())
	if err != nil {
		return nil, err
	}
	st, rerr := s.read()
	if err := errors.Join(rerr, release()); err != nil {
		return nil, err
	}
	return st, nil
}

// Apply runs m on a fresh snapshot and commits the result if the state is
// still at the snapshot's revision. With expected set (not AnyRevision) the
// snapshot must also be at that revision, and a stale commit is returned
// as a *StaleError instead of being retried.
func (s *Store) Apply(expected int64, m Mutation) (Result, error) {
	attempts := s.MaxAttempts
	if expected != AnyRevision || attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		var res Result
		res, err = s.applyOnce(expected, m)
		if !errors.Is(err, ErrStale) {
			return res, err
		}
	}
	return Result{}, err
}

func (s *Store) applyOnce(expected int64, m Mutation) (Result, error) {
	st, err := s.Load()
	if err != nil {
		return Result{}, err
	}
	base := st.Revision()
	if expected != AnyRevision && expected != base {
		return Result{}, &StaleError{Current: base, Expected: expected}
	}
	now := s.Now()
	out, err := m(st, now)
	if err != nil {
		return Result{}, err
	}
	if len(out.Events) == 0 {
		return Result{Outcome: out, Revision: base}, nil
	}
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if err := s.commit(base, st, out.Events, now); err != nil {
		return Result{}, err
	}
	return Result{Outcome: out, Revision: base + 1, Committed: true}, nil
}

func (s *Store) commit(base int64, st *State, events []Event, now time.Time) (err error) {
	release, err := s.lock(now)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	cur, err := s.read()
	if err != nil {
		return err
	}
	if cur.Revision() != base {
		return &StaleError{Current: cur.Revision(), Expected: base}
	}
	rev := base + 1
	st.setRevision(rev)
	if err := appendAudit(filepath.Join(s.Dir, AuditFile), rev, now, events); err != nil {
		return fmt.Errorf("append audit log, state left at revision %d: %w", base, err)
	}
	return s.write(st, rev)
}

func (s *Store) lock(now time.Time) (func() error, error) {
	return acquireLock(filepath.Join(s.Dir, LockFile), s.LockTimeout, now)
}

type stateFile struct {
	name string
	v    any
}

func (s *Store) read() (*State, error) {
	st := &State{}
	files := []stateFile{
		{RegistryFile, &st.Registry}, {AgentsFile, &st.Agents},
		{FilesFile, &st.Files}, {DepsFile, &st.Deps},
	}
	for _, f := range files {
		if err := decodeStrict(filepath.Join(s.Dir, f.name), f.v); err != nil {
			return nil, unreadable(err)
		}
	}
	if err := checkVersions(st); err != nil {
		return nil, unreadable(err)
	}
	if err := checkRevisions(st); err != nil {
		return nil, err
	}
	if st.Files.CurrentOwners == nil {
		st.Files.CurrentOwners = map[string]Reservation{}
	}
	return st, nil
}

func unreadable(err error) error {
	return fmt.Errorf("coordination state is unreadable, refusing to continue: %w", err)
}

func checkVersions(st *State) error {
	versions := []struct{ name, v string }{
		{RegistryFile, st.Registry.Version}, {AgentsFile, st.Agents.Version},
		{FilesFile, st.Files.Version}, {DepsFile, st.Deps.Version},
	}
	for _, f := range versions {
		if f.v != StateVersion {
			return fmt.Errorf("%s has unsupported version %q (want %q)", f.name, f.v, StateVersion)
		}
	}
	return nil
}

// checkRevisions requires the three written files to agree. They differ
// only when a write stopped part-way, which this package will not guess
// its way out of.
func checkRevisions(st *State) error {
	r, a, f := st.Registry.Revision, st.Agents.Revision, st.Files.Revision
	if r == a && a == f {
		return nil
	}
	return fmt.Errorf("coordination state is inconsistent, refusing to continue: revisions differ (%s %d, %s %d, %s %d); a write may have been interrupted, so restore the directory from git and retry", RegistryFile, r, AgentsFile, a, FilesFile, f)
}

func (s *Store) write(st *State, rev int64) error {
	files := []stateFile{{AgentsFile, &st.Agents}, {FilesFile, &st.Files}, {RegistryFile, &st.Registry}}
	for _, f := range files {
		if err := writeJSONAtomic(filepath.Join(s.Dir, f.name), f.v); err != nil {
			return fmt.Errorf("write coordination state revision %d (its audit entry is already recorded; run validate): %w", rev, err)
		}
	}
	return nil
}
