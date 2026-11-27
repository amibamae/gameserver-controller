//go:build !windows

package main

// Recovered CLI characterization uses temporary state and fake provider/SSH calls.
import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const pilotR3OperationID = "0123456789abcdef0123456789abcdef"

func pilotR3Run(t *testing.T, statePath string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runWithSettings(args, &stdout, &stderr, runtimeSettings{stateDBPath: statePath})
	return stdout.String(), stderr.String(), err
}

func pilotR3ErrorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func pilotR3Digest() string {
	digest := sha256.Sum256([]byte("bootstrap"))
	return hex.EncodeToString(digest[:])
}

// pilotR3Vultr is a fake Vultr API behind http.DefaultTransport, which the
// production client uses. It records each call as "METHOD /path".
type pilotR3Vultr struct {
	mu        sync.Mutex
	calls     []string
	responses map[string]func(body []byte) (int, string)
}

func (fake *pilotR3Vultr) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	key := request.Method + " " + request.URL.Path
	fake.mu.Lock()
	fake.calls = append(fake.calls, key)
	respond := fake.responses[key]
	fake.mu.Unlock()
	if request.URL.Host != "api.vultr.com" || respond == nil {
		return nil, fmt.Errorf("unexpected Vultr call %s %s", request.Method, request.URL)
	}
	status, text := respond(body)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: http.Header{"Content-Type": {"application/json"}}, Request: request}, nil
}

func (fake *pilotR3Vultr) recorded() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.calls...)
}

func pilotR3InstallVultr(t *testing.T, controllerKey string) *pilotR3Vultr {
	t.Helper()
	var mu sync.Mutex
	label := ""
	fake := &pilotR3Vultr{responses: map[string]func([]byte) (int, string){
		"GET /v2/startup-scripts/script-123": func([]byte) (int, string) {
			return 200, fmt.Sprintf(`{"startup_script":{"id":"script-123","name":"bootstrap","type":"boot","script":%q}}`, base64.StdEncoding.EncodeToString([]byte("bootstrap")))
		},
		"GET /v2/ssh-keys/sshkey-123": func([]byte) (int, string) {
			return 200, fmt.Sprintf(`{"ssh_key":{"id":"sshkey-123","name":"gsc","ssh_key":%q}}`, controllerKey)
		},
		"GET /v2/firewalls/firewall-123/rules": func([]byte) (int, string) {
			return 200, `{"firewall_rules":[{"id":1,"ip_type":"v4","action":"accept","protocol":"tcp","port":"22","subnet":"203.0.113.10","subnet_size":32,"source":""}],"meta":{"total":1,"links":{"next":""}}}`
		},
		"POST /v2/instances": func(body []byte) (int, string) {
			var request struct {
				Label string `json:"label"`
			}
			_ = json.Unmarshal(body, &request)
			mu.Lock()
			label = request.Label
			mu.Unlock()
			return 202, fmt.Sprintf(`{"instance":{"id":"instance-1","label":%q,"region":"ewr","plan":"vc2-1c-2gb","os_id":362}}`, request.Label)
		},
		"GET /v2/instances/instance-1": func([]byte) (int, string) {
			mu.Lock()
			defer mu.Unlock()
			return 200, fmt.Sprintf(`{"instance":{"id":"instance-1","label":%q,"region":"ewr","plan":"vc2-1c-2gb","os_id":362}}`, label)
		},
		"GET /v2/instances": func([]byte) (int, string) {
			return 200, `{"instances":[],"meta":{"total":0,"links":{"next":""}}}`
		},
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = fake
	t.Cleanup(func() { http.DefaultTransport = previous })
	return fake
}

// pilotR3Credentials writes a systemd credential directory and returns the
// controller's authorized public key.
func pilotR3Credentials(t *testing.T, withToken, withSSHKey bool) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", directory)
	public, private, err := sshkey.Generate()
	pilotR3RequireNoError(t, err)
	if withToken {
		pilotR3RequireNoError(t, os.WriteFile(filepath.Join(directory, "vultr-pilot-api-token"), []byte("test-token"), 0o600))
	}
	if withSSHKey {
		encoded, err := sshkey.MarshalPrivateKey(private, "controller")
		pilotR3RequireNoError(t, err)
		pilotR3RequireNoError(t, os.WriteFile(filepath.Join(directory, "gsc-pilot-ssh-key"), encoded, 0o600))
	}
	return sshkey.AuthorizedKey(public)
}

func pilotR3Audit(t *testing.T, home string) []auditRecord {
	t.Helper()
	file, err := os.Open(filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	pilotR3RequireNoError(t, err)
	defer file.Close()
	var records []auditRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record auditRecord
		pilotR3RequireNoError(t, json.Unmarshal(scanner.Bytes(), &record))
		records = append(records, record)
	}
	return records
}

// pilotR3AuditSummary renders records as "event|action|target|service|detail".
func pilotR3AuditSummary(records []auditRecord) []string {
	var lines []string
	for _, record := range records {
		lines = append(lines, strings.Join([]string{record.Event, record.Action, record.Target, record.Service, record.Detail}, "|"))
	}
	return lines
}

func pilotR3Equal(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n got: %q\nwant: %q", name, got, want)
	}
}

func pilotR3Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// pilotR3BrokenAuditHome makes HOME/.local a regular file so that creating
// the audit directory fails.
func pilotR3BrokenAuditHome(t *testing.T) string {
	t.Helper()
	home := pilotR3Home(t)
	pilotR3RequireNoError(t, os.WriteFile(filepath.Join(home, ".local"), []byte("not a directory"), 0o600))
	return filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
}

func pilotR3ExecuteArgs(extra ...string) []string {
	args := append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@staging", "--operation-id", pilotR3OperationID)
	return append(args, extra...)
}

func pilotR3RequireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
