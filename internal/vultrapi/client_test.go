package vultrapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		SSHKeyIDs: []string{"sshkey-123"},
		UserData:  base64.StdEncoding.EncodeToString([]byte("#cloud-config\n")),
		Tags:      []string{"managed-by-gsc", "environment-staging"},
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

func TestCreateInstanceSendsOneAuthenticatedRequest(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v2/instances" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization header = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type header = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode create body: %v", err)
		}
		if body["region"] != "ewr" || body["plan"] != "vc2-1c-2gb" || body["os_id"] != float64(362) {
			t.Errorf("unexpected create fields: %#v", body)
		}
		if body["label"] != "gsc-pilot-staging-001" || body["hostname"] != "staging-001.example.invalid" {
			t.Errorf("unexpected identity fields: %#v", body)
		}
		if body["user_data"] != testCreateInput().UserData {
			t.Errorf("user_data was not sent as base64")
		}
		if _, exists := body["ssh_key_ids"]; exists {
			t.Errorf("request used internal SSH field name instead of Vultr sshkey_id")
		}
		if got := body["sshkey_id"].([]any); len(got) != 1 || got[0] != "sshkey-123" {
			t.Errorf("unexpected sshkey_id: %#v", body["sshkey_id"])
		}
		if got := body["tags"].([]any); len(got) != 2 {
			t.Errorf("unexpected tags: %#v", body["tags"])
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
	if got := calls.Load(); got != 1 {
		t.Fatalf("create request count = %d, want 1", got)
	}
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
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			})
			_, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("error = %v, want ErrOutcomeUnknown", err)
			}
			if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "sensitive provider detail") {
				t.Fatalf("error disclosed a secret or provider response: %v", err)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("create request count = %d, want 1", got)
			}
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
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "transport detail") {
		t.Fatalf("error disclosed transport detail: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("create request count = %d, want 1", got)
	}
}

func TestCreateInstanceRejectsProvider4xxWithoutLeakingBody(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprint(w, `{"error":"private account detail"}`)
	})
	_, err := client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
	if !errors.Is(err, ErrProviderReject) {
		t.Fatalf("error = %v, want ErrProviderReject", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "private account detail") {
		t.Fatalf("error disclosed a secret or provider response: %v", err)
	}
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
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v2/instances" {
			t.Errorf("unexpected list request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization header = %q", got)
		}
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
	if got := calls.Load(); got != 2 {
		t.Fatalf("list request count = %d, want 2", got)
	}
}

func TestListInstancesByLabelRejectsRepeatedCursor(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"instances":[],"meta":{"links":{"next":"same"}}}`)
	})
	_, err := client.ListInstancesByLabel(context.Background(), []byte(testToken), "gsc-managed-42")
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestListInstancesByLabelRequiresPaginationMetadata(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"instances":[]}`)
	})
	_, err := client.ListInstancesByLabel(context.Background(), []byte(testToken), "gsc-managed-42")
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want ErrInvalidResponse", err)
	}
}

func TestGetInstanceUsesExactIDAndValidatesResponse(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/instances/inst-123" {
			t.Errorf("unexpected get request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"instance":{"id":"inst-123","label":"gsc-managed-42","date_created":"2026-11-07T10:00:00+00:00"}}`)
	})
	instance, err := client.GetInstance(context.Background(), []byte(testToken), "inst-123")
	if err != nil || instance.ID != "inst-123" || instance.DateCreated != "2026-11-07T10:00:00+00:00" {
		t.Fatalf("GetInstance = %#v, %v", instance, err)
	}
}

func TestInvalidCreateRequestsDoNotReachProvider(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
	})
	cases := map[string]func(*CreateInstanceRequest){
		"missing plan":      func(input *CreateInstanceRequest) { input.Plan = " " },
		"invalid os id":     func(input *CreateInstanceRequest) { input.OSID = 0 },
		"invalid label":     func(input *CreateInstanceRequest) { input.Label = "bad\nlabel" },
		"invalid SSH key":   func(input *CreateInstanceRequest) { input.SSHKeyIDs = []string{"bad/key"} },
		"invalid user data": func(input *CreateInstanceRequest) { input.UserData = "not base64" },
		"duplicate tag":     func(input *CreateInstanceRequest) { input.Tags = []string{"same", "same"} },
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
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
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
