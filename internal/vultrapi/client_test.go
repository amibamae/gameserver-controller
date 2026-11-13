package vultrapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "unit-test-vultr-token"

func testCreateInput() CreateInstanceRequest {
	return CreateInstanceRequest{
		Region: "ewr", Plan: "vc2-1c-2gb", OSID: 362,
		Label: "gsc-pilot-staging-001", Hostname: "staging-001.example.invalid",
		SSHKeyIDs: []string{"sshkey-123"}, StartupScriptID: "script-123",
		UserData: base64.StdEncoding.EncodeToString([]byte("#cloud-config\n")),
		Tags:     []string{"managed-by-gsc", "environment-staging"},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		incoming := request.Clone(request.Context())
		incoming.RequestURI = request.URL.RequestURI()
		handler.ServeHTTP(recorder, incoming)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})
	client, err := NewClientAt("https://api.example.invalid/v2", &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("NewClientAt: %v", err)
	}
	return client
}

// countingClient is testClient with a count of requests that reached handler.
func countingClient(t *testing.T, handler http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	})
	return client, &calls
}

// staticClient answers every request with body and a 200 status.
func staticClient(t *testing.T, body string) *Client {
	t.Helper()
	return testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) })
}

// expectAuthorized reports a request that is not method path, or that lacks
// the bearer token or the JSON Accept header.
func expectAuthorized(t *testing.T, r *http.Request, method, path string) {
	t.Helper()
	if r.Method != method || r.URL.Path != path {
		t.Errorf("unexpected request: %s %s, want %s %s", r.Method, r.URL.Path, method, path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization header = %q", got)
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept header = %q", got)
	}
}

// expectSafeError fails unless err wraps want and its text holds neither
// the API token nor any of the given provider details.
func expectSafeError(t *testing.T, err, want error, details ...string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	for _, detail := range append(details, testToken) {
		if strings.Contains(err.Error(), detail) {
			t.Fatalf("error disclosed %q: %v", detail, err)
		}
	}
}

// expectCalls fails unless exactly want requests were made.
func expectCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	if got := calls.Load(); got != want {
		t.Fatalf("request count = %d, want %d", got, want)
	}
}

func TestCreateInstanceSendsOneAuthenticatedRequest(t *testing.T) {
	// The exact body proves the Vultr field names (sshkey_id, not
	// ssh_key_ids) and that user_data is passed through as base64.
	wantBody := map[string]any{
		"region": "ewr", "plan": "vc2-1c-2gb", "os_id": float64(362),
		"label": "gsc-pilot-staging-001", "hostname": "staging-001.example.invalid",
		"sshkey_id": []any{"sshkey-123"}, "script_id": "script-123",
		"user_data": testCreateInput().UserData, "tags": []any{"managed-by-gsc", "environment-staging"},
	}
	client, calls := countingClient(t, func(w http.ResponseWriter, r *http.Request) {
		expectAuthorized(t, r, http.MethodPost, "/v2/instances")
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create body: %v", err)
		}
		if !reflect.DeepEqual(body, wantBody) {
			t.Errorf("create body = %#v, want %#v", body, wantBody)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintln(w, `{"instance":{"id":"inst-123","region":"ewr","plan":"vc2-1c-2gb","os_id":362,"label":"gsc-pilot-staging-001","status":"pending"}}`)
	})

	instance, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if instance.ID != "inst-123" || instance.Status != "pending" {
		t.Fatalf("unexpected created instance: %#v", instance)
	}
	expectCalls(t, calls, 1)
}

func TestCreateInstanceUnknownOutcomesAreNotRetried(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":"sensitive provider detail"}`},
		{name: "request timeout response", status: http.StatusRequestTimeout, body: `{"error":"request timed out"}`},
		{name: "malformed accepted response", status: http.StatusAccepted, body: `{"instance":{}}`},
		{name: "empty accepted response", status: http.StatusAccepted, body: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, calls := countingClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			})
			_, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
			expectSafeError(t, err, ErrOutcomeUnknown, "sensitive provider detail")
			expectCalls(t, calls, 1)
		})
	}
}

func TestCreateInstanceTransportFailureIsUnknownAndDoesNotLeakError(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("transport detail contains private token " + testToken)
	})
	client, err := NewClientAt("https://api.example.invalid/v2", &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("NewClientAt: %v", err)
	}
	_, err = client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
	expectSafeError(t, err, ErrOutcomeUnknown, "transport detail")
	expectCalls(t, &calls, 1)
}

func TestCreateInstanceRejectsProvider4xxWithoutLeakingBody(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprint(w, `{"error":"private account detail"}`)
	})
	_, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
	expectSafeError(t, err, ErrProviderReject, "private account detail")
}

func TestCreateInstanceDoesNotFollowRedirect(t *testing.T) {
	var redirectedCalls atomic.Int32
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "redirect.example.invalid" {
			redirectedCalls.Add(1)
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("authorization was forwarded to redirect target: %q", got)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = fmt.Fprintln(w, `{"instance":{"id":"inst-should-not-exist"}}`)
			return
		}
		calls.Add(1)
		http.Redirect(w, r, "https://redirect.example.invalid/v2/instances", http.StatusTemporaryRedirect)
	})
	_, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown", err)
	}
	if calls.Load() != 1 || redirectedCalls.Load() != 0 {
		t.Fatalf("source calls = %d, redirect calls = %d; want 1 and 0", calls.Load(), redirectedCalls.Load())
	}
}

func TestListInstancesByLabelPaginatesAndFiltersExactly(t *testing.T) {
	const cursor = "opaque cursor/with?reserved&chars"
	client, calls := countingClient(t, func(w http.ResponseWriter, r *http.Request) {
		expectAuthorized(t, r, http.MethodGet, "/v2/instances")
		if got := r.URL.Query().Get("label"); got != "gsc-managed-42" {
			t.Errorf("label query = %q", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") == "" {
			_, _ = fmt.Fprint(w, `{"instances":[{"id":"inst-1","label":"gsc-managed-42"},{"id":"unrelated","label":"gsc-managed-420"}],"meta":{"links":{"next":"opaque cursor/with?reserved&chars"}}}`)
			return
		}
		if got := r.URL.Query().Get("cursor"); got != cursor {
			t.Errorf("cursor = %q, want opaque cursor preserved", got)
		}
		_, _ = fmt.Fprint(w, `{"instances":[{"id":"inst-2","label":"gsc-managed-42"}],"meta":{"links":{"next":""}}}`)
	})

	instances, err := client.ListInstancesByLabel(context.Background(), []byte(testToken), "gsc-managed-42")
	if err != nil {
		t.Fatalf("ListInstancesByLabel: %v", err)
	}
	if len(instances) != 2 || instances[0].ID != "inst-1" || instances[1].ID != "inst-2" {
		t.Fatalf("unexpected exact-label instances: %#v", instances)
	}
	expectCalls(t, calls, 2)
}

func TestListInstancesByLabelRejectsRepeatedCursor(t *testing.T) {
	client := staticClient(t, `{"instances":[],"meta":{"links":{"next":"same"}}}`)
	_, err := client.ListInstancesByLabel(context.Background(), []byte(testToken), "gsc-managed-42")
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestListInstancesByLabelRequiresPaginationMetadata(t *testing.T) {
	client := staticClient(t, `{"instances":[]}`)
	_, err := client.ListInstancesByLabel(context.Background(), []byte(testToken), "gsc-managed-42")
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestGetInstanceUsesExactIDAndValidatesResponse(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		expectAuthorized(t, r, http.MethodGet, "/v2/instances/inst-123")
		_, _ = fmt.Fprint(w, `{"instance":{"id":"inst-123","label":"gsc-managed-42","date_created":"2026-11-07T10:00:00+00:00"}}`)
	})
	instance, err := client.GetInstance(context.Background(), []byte(testToken), "inst-123")
	if err != nil || instance.ID != "inst-123" || instance.DateCreated != "2026-11-07T10:00:00+00:00" {
		t.Fatalf("GetInstance = %#v, %v", instance, err)
	}
}

func TestInvalidCreateRequestsDoNotReachProvider(t *testing.T) {
	client, calls := countingClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	cases := map[string]func(*CreateInstanceRequest){
		"missing plan":              func(input *CreateInstanceRequest) { input.Plan = " " },
		"invalid os id":             func(input *CreateInstanceRequest) { input.OSID = 0 },
		"invalid label":             func(input *CreateInstanceRequest) { input.Label = "bad\nlabel" },
		"invalid SSH key":           func(input *CreateInstanceRequest) { input.SSHKeyIDs = []string{"bad/key"} },
		"invalid startup script ID": func(input *CreateInstanceRequest) { input.StartupScriptID = "../script" },
		"invalid user data":         func(input *CreateInstanceRequest) { input.UserData = "not base64" },
		"duplicate tag":             func(input *CreateInstanceRequest) { input.Tags = []string{"same", "same"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := testCreateInput()
			mutate(&input)
			if _, err := client.CreateInstance(context.Background(), []byte(testToken), input); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}
	expectCalls(t, calls, 0)
}

func TestNewClientAtDoesNotMutateCallerHTTPClient(t *testing.T) {
	redirectPolicyCalled := false
	original := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirectPolicyCalled = true
			return nil
		},
	}
	client, err := NewClientAt("https://api.example.invalid/v2", original)
	if err != nil {
		t.Fatalf("NewClientAt: %v", err)
	}
	if original.Timeout != 5*time.Second || original.CheckRedirect == nil {
		t.Fatal("NewClientAt mutated caller's HTTP client")
	}
	if err := original.CheckRedirect(nil, nil); err != nil || !redirectPolicyCalled {
		t.Fatal("NewClientAt replaced caller's redirect policy")
	}
	if client.http == original || client.http.Timeout != 5*time.Second || client.http.CheckRedirect == nil {
		t.Fatal("client did not use a protected copy of the HTTP client")
	}
}

func TestParseOSID(t *testing.T) {
	if got, err := ParseOSID("362"); err != nil || got != 362 {
		t.Fatalf("ParseOSID(362) = %d, %v", got, err)
	}
	for _, value := range []string{"", "0", "-3", "linux", " 362"} {
		if _, err := ParseOSID(value); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("ParseOSID(%q) error = %v, want ErrInvalidRequest", value, err)
		}
	}
}

func TestNewClientAtRequiresHTTPSV2URL(t *testing.T) {
	for _, value := range []string{
		"http://api.example.invalid/v2",
		"https://user:pass@api.example.invalid/v2",
		"https://api.example.invalid/v1",
		"https://api.example.invalid/v2?token=secret",
	} {
		if _, err := NewClientAt(value, nil); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("NewClientAt(%q) error = %v, want ErrInvalidRequest", value, err)
		}
	}
}
