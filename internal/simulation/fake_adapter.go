package simulation

import (
	"context"
	"encoding/json"
)

type fakeAdapter struct {
	caps []string
	fail bool
}

func (f *fakeAdapter) Capabilities() []string { return f.caps }

func (f *fakeAdapter) Invoke(ctx context.Context, name string, params json.RawMessage) (json.RawMessage, error) {
	if f.fail {
		return nil, &Error{Code: "simulated_failure", Message: "test adapter failure"}
	}
	data, _ := json.Marshal(map[string]string{
		"status":  "success",
		"adapter": name,
	})
	return json.RawMessage(data), nil
}
