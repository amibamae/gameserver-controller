package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// referenceEd25519Key reports whether line is exactly "ssh-ed25519 <base64>"
// with canonical base64 of one ed25519 wire key (hosting section 6.1).
func referenceEd25519Key(line string) bool {
	parts := strings.Split(line, " ")
	if len(parts) != 2 || parts[0] != "ssh-ed25519" {
		return false
	}
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || base64.StdEncoding.EncodeToString(blob) != parts[1] {
		return false
	}
	var want bytes.Buffer
	for _, field := range [][]byte{[]byte("ssh-ed25519"), nil} {
		if field == nil {
			if len(blob) != want.Len()+4+32 {
				return false
			}
			field = blob[want.Len()+4:]
		}
		_ = binary.Write(&want, binary.BigEndian, uint32(len(field)))
		want.Write(field)
	}
	return bytes.Equal(blob, want.Bytes())
}

// evaluateHostKeyHandoff is the reference evaluator for the single-use host
// key handoff: verify, rotate, pin, then send secrets only to the pinned key.
func evaluateHostKeyHandoff(_ *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	handoff := asObject(input["handoff"])
	expires, err := time.Parse(time.RFC3339, stringValue(handoff, "expires_at"))
	if err != nil {
		return nil, fmt.Errorf("handoff expires_at: %w", err)
	}
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluation := asObject(raw)
		m := handoffMachine{oneTime: stringValue(handoff, "one_time_host_key"), expires: expires, state: "pending_rotation"}
		steps := []any{}
		for _, rawStep := range arrayValue(evaluation, "steps") {
			step := asObject(rawStep)
			result, err := m.apply(step)
			if err != nil {
				return nil, err
			}
			steps = append(steps, map[string]any{"step": stringValue(step, "step"), "result": result})
		}
		var pinnedValue any
		if m.pinned != "" {
			pinnedValue = m.pinned
		}
		evaluations = append(evaluations, map[string]any{
			"case_id": stringValue(evaluation, "case_id"), "steps": steps, "final_state": m.state,
			"pinned_host_key": pinnedValue, "user_data_replaced": m.replaced,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

// handoffMachine is the controller's side of the single-use host key
// handoff (hosting section 6.1). state is pending_rotation until a rotation
// pins a new key, then rotated. session is the verified SSH session, if
// any: "one_time" or "pinned".
type handoffMachine struct {
	oneTime       string
	expires       time.Time
	state, pinned string
	session       string
	replaced      bool
}

// apply runs one step and returns its result.
func (m *handoffMachine) apply(step map[string]any) (string, error) {
	switch kind := stringValue(step, "step"); kind {
	case "connect_one_time":
		return m.connectOneTime(step)
	case "rotate":
		return m.rotate(step), nil
	case "replace_user_data":
		return m.replaceUserData(step), nil
	case "connect_pinned":
		return m.connectPinned(step), nil
	case "send_secret":
		return map[bool]string{true: "permitted", false: "refused"}[m.session == "pinned"], nil
	default:
		return "", fmt.Errorf("unknown handoff step %q", kind)
	}
}

// connectOneTime: before rotation and expiry, only the exact one-time key
// opens a verified session.
func (m *handoffMachine) connectOneTime(step map[string]any) (string, error) {
	at, err := time.Parse(time.RFC3339, stringValue(step, "at"))
	if err != nil {
		return "", fmt.Errorf("step at: %w", err)
	}
	result := "permitted"
	switch {
	case m.state != "pending_rotation":
		result = "not_pending"
	case !at.Before(m.expires):
		result = "expired"
	case stringValue(step, "presented_host_key") != m.oneTime:
		result = "host_key_mismatch"
	}
	m.session = map[bool]string{true: "one_time"}[result == "permitted"]
	return result, nil
}

// rotate: over a verified one-time session, the output must be one new
// ed25519 key, which is then pinned. The session ends either way.
func (m *handoffMachine) rotate(step map[string]any) string {
	output := strings.TrimSuffix(stringValue(step, "output"), "\n")
	result := "pinned"
	switch {
	case m.session != "one_time":
		result = "not_verified"
	case strings.ContainsAny(output, "\r\n") || !referenceEd25519Key(output):
		result = "invalid_output"
	case output == m.oneTime:
		result = "reused_one_time_key"
	default:
		m.state, m.pinned = "rotated", output
	}
	m.session = ""
	return result
}

// replaceUserData: the provider copy of the one-time key is replaced only
// after rotation.
func (m *handoffMachine) replaceUserData(step map[string]any) string {
	switch {
	case m.state != "rotated":
		return "not_rotated"
	case boolValue(step, "succeeded"):
		m.replaced = true
		return "replaced"
	default:
		return "not_replaced"
	}
}

// connectPinned: after rotation, only the pinned key opens a verified
// session.
func (m *handoffMachine) connectPinned(step map[string]any) string {
	result := "permitted"
	switch {
	case m.state != "rotated":
		result = "not_rotated"
	case stringValue(step, "presented_host_key") != m.pinned:
		result = "host_key_mismatch"
	}
	m.session = map[bool]string{true: "pinned"}[result == "permitted"]
	return result
}

func init() {
	registerEvaluator(evaluateHostKeyHandoff, "hosting.bootstrap.host-key-handoff")
}
