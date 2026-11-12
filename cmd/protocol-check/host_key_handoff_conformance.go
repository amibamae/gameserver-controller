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
	oneTime := stringValue(handoff, "one_time_host_key")
	expires, err := time.Parse(time.RFC3339, stringValue(handoff, "expires_at"))
	if err != nil {
		return nil, fmt.Errorf("handoff expires_at: %w", err)
	}
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluation := asObject(raw)
		state, pinned, session, replaced := "pending_rotation", "", "", false
		steps := []any{}
		for _, rawStep := range arrayValue(evaluation, "steps") {
			step := asObject(rawStep)
			var result string
			switch kind := stringValue(step, "step"); kind {
			case "connect_one_time":
				at, err := time.Parse(time.RFC3339, stringValue(step, "at"))
				if err != nil {
					return nil, fmt.Errorf("step at: %w", err)
				}
				switch {
				case state != "pending_rotation":
					result = "not_pending"
				case !at.Before(expires):
					result = "expired"
				case stringValue(step, "presented_host_key") != oneTime:
					result = "host_key_mismatch"
				default:
					result = "permitted"
				}
				session = map[bool]string{true: "one_time"}[result == "permitted"]
			case "rotate":
				output := strings.TrimSuffix(stringValue(step, "output"), "\n")
				switch {
				case session != "one_time":
					result = "not_verified"
				case strings.ContainsAny(output, "\r\n") || !referenceEd25519Key(output):
					result = "invalid_output"
				case output == oneTime:
					result = "reused_one_time_key"
				default:
					result, state, pinned = "pinned", "rotated", output
				}
				session = ""
			case "replace_user_data":
				switch {
				case state != "rotated":
					result = "not_rotated"
				case boolValue(step, "succeeded"):
					result, replaced = "replaced", true
				default:
					result = "not_replaced"
				}
			case "connect_pinned":
				switch {
				case state != "rotated":
					result = "not_rotated"
				case stringValue(step, "presented_host_key") != pinned:
					result = "host_key_mismatch"
				default:
					result = "permitted"
				}
				session = map[bool]string{true: "pinned"}[result == "permitted"]
			case "send_secret":
				result = map[bool]string{true: "permitted", false: "refused"}[session == "pinned"]
			default:
				return nil, fmt.Errorf("unknown handoff step %q", kind)
			}
			steps = append(steps, map[string]any{"step": stringValue(step, "step"), "result": result})
		}
		var pinnedValue any
		if pinned != "" {
			pinnedValue = pinned
		}
		evaluations = append(evaluations, map[string]any{
			"case_id": stringValue(evaluation, "case_id"), "steps": steps, "final_state": state,
			"pinned_host_key": pinnedValue, "user_data_replaced": replaced,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func init() {
	registerEvaluator(evaluateHostKeyHandoff, "hosting.bootstrap.host-key-handoff")
}
