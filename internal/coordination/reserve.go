package coordination

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

// Reserve gives agent exclusive use of the file patterns for task, which
// agent must hold; an empty task means the one task agent holds. It fails
// if any pattern overlaps a live reservation of another agent; overlap is
// judged conservatively (see PatternsOverlap).
func Reserve(agent, task string, patterns []string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		task, err := st.reserveTarget(agent, task, now)
		if err != nil {
			return Outcome{}, err
		}
		a, t, err := liveClaim(st, agent, task, now)
		if err != nil {
			return Outcome{}, err
		}
		normalized, err := normalizePatterns(patterns)
		if err != nil {
			return Outcome{}, err
		}
		for _, p := range normalized {
			if err := st.checkReservable(p, agent, now); err != nil {
				return Outcome{}, err
			}
		}
		stamp := formatTime(now)
		for _, p := range normalized {
			st.Files.CurrentOwners[p] = Reservation{Agent: agent, Task: task, ReservedAt: stamp}
		}
		st.refreshClaim(t, a, now)
		detail := "reserved " + strings.Join(normalized, ", ") + " until " + *t.ExpiresAt
		return Outcome{Events: []Event{{agent, "reserve", task, detail}}}, nil
	}
}

func (s *State) reserveTarget(agent, task string, now time.Time) (string, error) {
	if task != "" {
		return task, nil
	}
	held, err := s.heldTask(agent, "", now)
	if err != nil {
		return "", err
	}
	if held == "" {
		return "", fmt.Errorf("agent %s holds no task to reserve files for; claim one or name it with --for", agent)
	}
	return held, nil
}

func normalizePatterns(patterns []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range patterns {
		p, err := NormalizePattern(raw)
		if err != nil {
			return nil, err
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no files to reserve")
	}
	sort.Strings(out)
	return out, nil
}

// NormalizePattern cleans a repository-relative file pattern and rejects
// empty, absolute and escaping ones and malformed globs.
func NormalizePattern(raw string) (string, error) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	p := path.Clean(s)
	if invalidPattern(s, p) {
		return "", fmt.Errorf("invalid file pattern %q: use a path relative to the repository root", raw)
	}
	if _, err := path.Match(p, ""); err != nil {
		return "", fmt.Errorf("invalid file pattern %q: %v", raw, err)
	}
	return p, nil
}

func invalidPattern(raw, cleaned string) bool {
	return raw == "" || cleaned == "." || escapesRoot(cleaned) || isAbsolute(cleaned)
}

func escapesRoot(p string) bool { return p == ".." || strings.HasPrefix(p, "../") }

func isAbsolute(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) > 1 && p[1] == ':')
}

func (s *State) checkReservable(p, agent string, now time.Time) error {
	for key, r := range s.Files.CurrentOwners {
		if r.Agent == agent || !PatternsOverlap(key, p) {
			continue
		}
		expired, err := stampExpired("reservation "+key, r.ExpiresAt, now)
		if err != nil {
			return err
		}
		if !expired {
			return fmt.Errorf("file %s overlaps %s, reserved by %s for %s until %s", p, key, r.Agent, r.Task, r.ExpiresAt)
		}
	}
	return nil
}

// PatternsOverlap reports whether two normalized patterns may name a
// common file. Each pattern is reduced to a scope: the literal text before
// its first glob character, or the path plus "/" when it has none (so a
// plain directory covers its subtree). Two patterns overlap when one scope
// is a prefix of the other. This can report overlap where none exists; it
// never misses one.
func PatternsOverlap(a, b string) bool {
	sa, sb := patternScope(a), patternScope(b)
	return strings.HasPrefix(sa, sb) || strings.HasPrefix(sb, sa)
}

func patternScope(p string) string {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return p[:i]
	}
	return p + "/"
}
