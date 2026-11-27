package coordination

import (
	"reflect"
	"testing"
	"time"
)

func TestPatternsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"cmd/a.go", "cmd/a.go", true},
		{"cmd/a.go", "cmd/b.go", false},
		{"cmd/foo.go", "cmd/foo.go.bak", false},
		{"internal/vultrapi", "internal/vultrapi/client.go", true},
		{"internal/vultrapi", "internal/vultrapi2/client.go", false},
		{"cmd/*.go", "cmd/x.go", true},
		{"cmd/*.go", "cmd2/x.go", false},
		{"cmd/a*.go", "cmd/b.go", false},
		{"cmd/a*.go", "cmd/ab.go", true},
		{"internal/*", "internal/controller/*.go", true},
		{"x/[ab].go", "x/c.go", true}, // conservative: never misses an overlap
	}
	for _, tc := range cases {
		if got := PatternsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("PatternsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := PatternsOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("PatternsOverlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestNormalizePattern(t *testing.T) {
	good := map[string]string{
		`internal\controller\*.go`: "internal/controller/*.go",
		" ./cmd//x/../a.go ":       "cmd/a.go",
	}
	for in, want := range good {
		if got, err := NormalizePattern(in); err != nil || got != want {
			t.Errorf("NormalizePattern(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":          `invalid file pattern "": use a path relative to the repository root`,
		".":         `invalid file pattern ".": use a path relative to the repository root`,
		"../x":      `invalid file pattern "../x": use a path relative to the repository root`,
		"..":        `invalid file pattern "..": use a path relative to the repository root`,
		"/etc/x":    `invalid file pattern "/etc/x": use a path relative to the repository root`,
		`C:\x`:      `invalid file pattern "C:\\x": use a path relative to the repository root`,
		"cmd/[a.go": `invalid file pattern "cmd/[a.go": syntax error in pattern`,
	}
	for in, want := range bad {
		_, err := NormalizePattern(in)
		wantErr(t, err, want)
	}
}

func TestReserveRecordsOwners(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	res := mustApply(t, s, Reserve("a1", "", []string{"x/b.go", `x\a.go`, "x/b.go"}))
	if want := "reserved x/a.go, x/b.go until " + ts(ClaimTTL); res.Events[0].Detail != want {
		t.Fatalf("detail = %q", res.Events[0].Detail)
	}
	want := map[string]Reservation{
		"x/a.go": {Agent: "a1", Task: "T1", ReservedAt: ts(0), ExpiresAt: ts(ClaimTTL)},
		"x/b.go": {Agent: "a1", Task: "T1", ReservedAt: ts(0), ExpiresAt: ts(ClaimTTL)},
	}
	if got := mustLoad(t, s).Files.CurrentOwners; !reflect.DeepEqual(got, want) {
		t.Fatalf("owners = %+v", got)
	}
	// The named shared areas are kept as they were.
	if got := mustLoad(t, s).Files.Reservations; !reflect.DeepEqual(got, map[string][]string{"area": {"x/*"}}) {
		t.Fatalf("reservations = %v", got)
	}
	// Reserving an overlapping pattern of one's own is allowed.
	mustApply(t, s, Reserve("a1", "T1", []string{"x/*"}))
}

func TestReserveConflicts(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Claim("a2", "T2"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/*"}))
	wantApplyErr(t, s, Reserve("a2", "T2", []string{"y/ok.go", "x/a.go"}),
		"file x/a.go overlaps x/*, reserved by a1 for T1 until "+ts(ClaimTTL))
	// Once a1's reservation expires, a2 may take the files. a2 heartbeats
	// first so its own claim is still live.
	c.advance(ClaimTTL - time.Minute)
	mustApply(t, s, Update("a2", "T2", "alive", false))
	c.advance(time.Minute)
	mustApply(t, s, Reserve("a2", "T2", []string{"x/a.go"}))
}

func TestReserveRejections(t *testing.T) {
	s, _ := newTestStore(t)
	wantApplyErr(t, s, Reserve("a1", "", []string{"x"}),
		"agent a1 holds no task to reserve files for; claim one or name it with --for")
	wantApplyErr(t, s, Reserve("a1", "T1", []string{"x"}), "task T1 is available, not claimed")
	mustApply(t, s, Claim("a1", "T1"))
	wantApplyErr(t, s, Reserve("a1", "T1", nil), "no files to reserve")
	wantApplyErr(t, s, Reserve("a1", "T1", []string{"../x"}),
		`invalid file pattern "../x": use a path relative to the repository root`)
	editRegistry(t, s, func(st *State) {
		st.Files.CurrentOwners["x/a.go"] = Reservation{Agent: "a2", Task: "T2", ExpiresAt: "bad"}
	})
	wantApplyErr(t, s, Reserve("a1", "T1", []string{"x/a.go"}), `reservation x/a.go has invalid expiry "bad"`)
	editRegistry(t, s, func(st *State) { st.Task("T2").ExpiresAt = strPtr("bad") })
	st := mustLoad(t, s)
	st.Task("T2").Status = StatusClaimed
	st.Task("T2").AssignedTo = strPtr("a3")
	_, err := Reserve("a3", "", []string{"y"})(st, t0)
	wantErr(t, err, `task T2 has invalid expiry "bad"`)
}
