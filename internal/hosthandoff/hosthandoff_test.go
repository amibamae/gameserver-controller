package hosthandoff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMatchesTheHandoffVector runs every published case through the state
// machine the pilot uses.
func TestMatchesTheHandoffVector(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "hosting-bootstrap-host-key-handoff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Input struct {
			Handoff struct {
				OneTimeHostKey string    `json:"one_time_host_key"`
				CreatedAt      time.Time `json:"created_at"`
				ExpiresAt      time.Time `json:"expires_at"`
			} `json:"handoff"`
			Evaluations []struct {
				CaseID string `json:"case_id"`
				Steps  []struct {
					Step      string    `json:"step"`
					At        time.Time `json:"at"`
					Presented string    `json:"presented_host_key"`
					Output    string    `json:"output"`
					Succeeded bool      `json:"succeeded"`
				} `json:"steps"`
			} `json:"evaluations"`
		} `json:"input"`
		Expected struct {
			Evaluations []struct {
				Steps []struct {
					Result string `json:"result"`
				} `json:"steps"`
				FinalState       State   `json:"final_state"`
				PinnedHostKey    *string `json:"pinned_host_key"`
				UserDataReplaced bool    `json:"user_data_replaced"`
			} `json:"evaluations"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	for i, evaluation := range vector.Input.Evaluations {
		t.Run(evaluation.CaseID, func(t *testing.T) {
			h := Handoff{OneTimeHostKey: vector.Input.Handoff.OneTimeHostKey, CreatedAt: vector.Input.Handoff.CreatedAt,
				ExpiresAt: vector.Input.Handoff.ExpiresAt, State: PendingRotation}
			want := vector.Expected.Evaluations[i]
			for j, step := range evaluation.Steps {
				var got string
				switch step.Step {
				case "connect_one_time":
					got = h.ConnectOneTime(step.Presented, step.At)
				case "rotate":
					got = h.Rotate(step.Output)
				case "replace_user_data":
					got = h.ReplaceUserData(step.Succeeded)
				case "connect_pinned":
					got = h.ConnectPinned(step.Presented)
				case "send_secret":
					got = h.SendSecret()
				default:
					t.Fatalf("unknown step %q", step.Step)
				}
				if got != want.Steps[j].Result {
					t.Errorf("step %d %s = %s, want %s", j, step.Step, got, want.Steps[j].Result)
				}
			}
			pinned := ""
			if want.PinnedHostKey != nil {
				pinned = *want.PinnedHostKey
			}
			if h.State != want.FinalState || h.PinnedHostKey != pinned || h.UserDataReplaced != want.UserDataReplaced {
				t.Errorf("final = %s %q %v", h.State, h.PinnedHostKey, h.UserDataReplaced)
			}
			if err := h.Validate(); err != nil {
				t.Errorf("final record invalid: %v", err)
			}
		})
	}
}

func TestSessionIsNotPersisted(t *testing.T) {
	h := Handoff{OneTimeHostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea",
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), State: PendingRotation}
	if h.ConnectOneTime(h.OneTimeHostKey, time.Now()) != Permitted {
		t.Fatal("connect refused")
	}
	encoded, _ := json.Marshal(h)
	var loaded Handoff
	_ = json.Unmarshal(encoded, &loaded)
	if loaded.Rotate("x") != NotVerified {
		t.Fatal("a reloaded record must not carry a verified session")
	}
}
