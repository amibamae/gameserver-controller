package cloudflaredns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

const (
	testZoneID = "0123456789abcdef0123456789abcdef"
	testToken  = "cf-test-token"
	tokenRef   = "systemd-credential://cloudflare-dns-token"
)

var (
	testNow  = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	instance = ResourceRef{AdapterID: "hosting.vultr", EnvironmentID: "test", ResourceKind: "instance", ExternalID: "instance-test-001"}
)

type staticCredential string

func (c staticCredential) WithCredential(_ string, use func([]byte) error) error {
	return use([]byte(c))
}

// fakeCloudflare is an in-memory Cloudflare DNS API for one zone.
type fakeCloudflare struct {
	mu        sync.Mutex
	records   map[string]dnsRecord
	next      int
	mutations []string
	// failMutation makes the Nth mutating call (1-based) reply with status.
	failMutation int
	failStatus   int
	// ignoreWrites accepts writes without applying them.
	ignoreWrites bool
	listStatus   int
}

func newFakeCloudflare() *fakeCloudflare {
	return &fakeCloudflare{records: map[string]dnsRecord{}}
}

func (f *fakeCloudflare) add(name, recordType, content string, ttl int, proxied bool) {
	f.next++
	id := fmt.Sprintf("%032x", f.next)
	f.records[id] = dnsRecord{ID: id, Type: recordType, Name: name, Content: content, TTL: ttl, Proxied: proxied}
}

func (f *fakeCloudflare) values(name, recordType string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var values []string
	for _, record := range f.records {
		if record.Name == name && record.Type == recordType {
			values = append(values, fmt.Sprintf("%s/%d/%v", record.Content, record.TTL, record.Proxied))
		}
	}
	return values
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	prefix := "/client/v4/zones/" + testZoneID + "/dns_records"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	reply := func(result any) {
		data, _ := json.Marshal(result)
		_, _ = fmt.Fprintf(w, `{"success":true,"errors":[],"result":%s}`, data)
	}
	if r.Method == http.MethodGet {
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			return
		}
		result := []dnsRecord{}
		for _, record := range f.records {
			if record.Name == r.URL.Query().Get("name") && record.Type == r.URL.Query().Get("type") {
				result = append(result, record)
			}
		}
		reply(result)
		return
	}
	f.mutations = append(f.mutations, r.Method)
	if f.failMutation == len(f.mutations) {
		w.WriteHeader(f.failStatus)
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":1004,"message":"rejected"}]}`)
		return
	}
	if f.ignoreWrites {
		reply(map[string]any{})
		return
	}
	switch r.Method {
	case http.MethodPost:
		var record dnsRecord
		_ = json.NewDecoder(r.Body).Decode(&record)
		f.next++
		record.ID = fmt.Sprintf("%032x", f.next)
		f.records[record.ID] = record
		reply(record)
	case http.MethodPatch:
		var patch struct {
			TTL     int  `json:"ttl"`
			Proxied bool `json:"proxied"`
		}
		_ = json.NewDecoder(r.Body).Decode(&patch)
		record := f.records[id]
		record.TTL, record.Proxied = patch.TTL, patch.Proxied
		f.records[id] = record
		reply(record)
	case http.MethodDelete:
		delete(f.records, id)
		reply(map[string]string{"id": id})
	}
}

type harness struct {
	t        *testing.T
	fake     *fakeCloudflare
	impl     *Adapter
	registry *adapter.Registry
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeCloudflare()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	config := Config{
		AdapterID: "integration.cloudflare-dns", EnvironmentID: "test", ZoneID: testZoneID, ZoneName: "example.com", APITokenRef: tokenRef,
		Records: map[string]ManagedRecord{
			"paper-main.backends.example.com": {Purposes: []Purpose{PurposeBackendAddress}},
			"play.example.com":                {Purposes: []Purpose{PurposeDirectJoin, PurposeFallback}, Fallback: map[string][]string{"A": {"198.51.100.10"}}},
		},
	}
	impl, err := newAdapter(config, schemas.ProtocolVersion(), staticCredential(testToken+"\n"), nil, server.URL+"/client/v4", func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(impl); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, fake: fake, impl: impl, registry: registry}
}

func (h *harness) invoke(capability string, input any) (adapter.InvokeResponse, map[string]any, error) {
	h.t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		h.t.Fatal(err)
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: "request-1", Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: "0.21.0-draft", Capability: capability, Input: encoded,
		RequestContext: adapter.RequestContext{RequestID: "request-1", CorrelationID: "correlation-1", EnvironmentID: "test",
			OperationID: "operation-1", StepID: "step-1", Deadline: time.Now().Add(time.Minute).UTC(),
			SecretRefs: []adapter.SecretReference{{SecretRef: tokenRef, Purpose: "Cloudflare DNS edit"}}},
	}}
	response, err := h.registry.Invoke(context.Background(), "integration.cloudflare-dns", request)
	if err != nil {
		return response, nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(response.Result.Output, &output); err != nil {
		h.t.Fatal(err)
	}
	return response, output, nil
}

func (h *harness) mustInvoke(capability string, input any) (adapter.InvokeResponse, map[string]any) {
	h.t.Helper()
	response, output, err := h.invoke(capability, input)
	if err != nil {
		h.t.Fatalf("%s: %v", capability, err)
	}
	return response, output
}

func (h *harness) target(name string) recordTarget {
	return recordTarget{ZoneRef: h.impl.ZoneRef(), Name: name, RecordType: "A"}
}

func (h *harness) backendUpsert(values ...string) upsertInput {
	ref := instance
	return upsertInput{Record: h.target("paper-main.backends.example.com"), EnvironmentID: "test", Purpose: PurposeBackendAddress,
		ServerID: "minecraft-test", InstanceRef: &ref, Values: values, TTLSeconds: 60}
}

func (h *harness) joinUpsert(expires time.Time) upsertInput {
	ref := instance
	return upsertInput{Record: h.target("play.example.com"), EnvironmentID: "test", Purpose: PurposeDirectJoin, ServerID: "fivem-test", InstanceRef: &ref,
		ReadinessAssertion: &readinessAssertion{AssertionID: "readiness-001", ServerID: "fivem-test", EnvironmentID: "test", InstanceRef: instance,
			ReadinessCheckSetID: "fivem-ready-v1", ObservedAt: testNow.Add(-time.Minute), ExpiresAt: expires},
		Values: []string{"203.0.113.20"}, TTLSeconds: 60}
}

func expect(t *testing.T, response adapter.InvokeResponse, output map[string]any, outcome, state string, envelope adapter.Outcome) {
	t.Helper()
	if output["request_outcome"] != outcome || output["observation_state"] != state || response.Result.Outcome != envelope {
		t.Fatalf("got %v/%v (%s), want %s/%s (%s): %v", output["request_outcome"], output["observation_state"], response.Result.Outcome, outcome, state, envelope, output)
	}
}

func TestDescriptorMatchesProtocolExample(t *testing.T) {
	h := newHarness(t)
	data, err := os.ReadFile("../../protocol/examples/adapter-descriptor.cloudflare-dns.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(h.impl.Descriptor())
	_ = json.Unmarshal(encoded, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor differs from protocol example:\n got %s\nwant %s", encoded, data)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	base := Config{AdapterID: "integration.cloudflare-dns", EnvironmentID: "test", ZoneID: testZoneID, ZoneName: "example.com", APITokenRef: tokenRef,
		Records: map[string]ManagedRecord{"play.example.com": {Purposes: []Purpose{PurposeDirectJoin}}}}
	cases := map[string]func(*Config){
		"non-integration ID":   func(c *Config) { c.AdapterID = "hosting.cloudflare" },
		"bad zone ID":          func(c *Config) { c.ZoneID = "../zones" },
		"token not credential": func(c *Config) { c.APITokenRef = "raw-token" },
		"no records":           func(c *Config) { c.Records = nil },
		"record outside zone": func(c *Config) {
			c.Records = map[string]ManagedRecord{"play.example.net": {Purposes: []Purpose{PurposeDirectJoin}}}
		},
		"unknown purpose": func(c *Config) {
			c.Records = map[string]ManagedRecord{"play.example.com": {Purposes: []Purpose{"cname"}}}
		},
		"fallback w/o values": func(c *Config) {
			c.Records = map[string]ManagedRecord{"play.example.com": {Purposes: []Purpose{PurposeFallback}}}
		},
		"fallback wrong family": func(c *Config) {
			c.Records = map[string]ManagedRecord{"play.example.com": {Purposes: []Purpose{PurposeFallback}, Fallback: map[string][]string{"A": {"2001:db8::1"}}}}
		},
	}
	for name, mutate := range cases {
		config := base
		mutate(&config)
		if _, err := New(config, "0.21.0-draft", staticCredential("x"), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(base, "0.21.0-draft", staticCredential("x"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestInspect(t *testing.T) {
	h := newHarness(t)
	_, output := h.mustInvoke("integration.dns.record.inspect", inspectInput{Record: h.target("paper-main.backends.example.com"), EnvironmentID: "test"})
	if output["observation_state"] != "absent" {
		t.Fatalf("empty record: %v", output)
	}
	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.20", 60, false)
	_, output = h.mustInvoke("integration.dns.record.inspect", inspectInput{Record: h.target("paper-main.backends.example.com"), EnvironmentID: "test"})
	if output["observation_state"] != "present" || output["ttl_seconds"] != float64(60) || output["values"].([]any)[0] != "203.0.113.20" {
		t.Fatalf("present record: %v", output)
	}
	h.fake.listStatus = http.StatusBadGateway
	_, output = h.mustInvoke("integration.dns.record.inspect", inspectInput{Record: h.target("paper-main.backends.example.com"), EnvironmentID: "test"})
	if output["observation_state"] != "unknown" {
		t.Fatalf("API failure must be unknown: %v", output)
	}
}

func TestRequestsOutsideTheManagedScopeAreRefused(t *testing.T) {
	h := newHarness(t)
	other := h.target("www.example.com")
	if _, _, err := h.invoke("integration.dns.record.inspect", inspectInput{Record: other, EnvironmentID: "test"}); err == nil {
		t.Fatal("unmanaged name accepted")
	}
	if _, _, err := h.invoke("integration.dns.record.inspect", inspectInput{Record: h.target("play.example.com"), EnvironmentID: "production"}); err == nil {
		t.Fatal("other environment accepted")
	}
	wrongZone := h.target("play.example.com")
	wrongZone.ZoneRef.ExternalID = "fedcba9876543210fedcba9876543210"
	if _, _, err := h.invoke("integration.dns.record.inspect", inspectInput{Record: wrongZone, EnvironmentID: "test"}); err == nil {
		t.Fatal("other zone accepted")
	}
	// The token reference must be authorized in the request context.
	encoded, _ := json.Marshal(inspectInput{Record: h.target("play.example.com"), EnvironmentID: "test"})
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: "r", Method: "adapter.invoke", Params: adapter.InvokeParams{ProtocolVersion: "0.21.0-draft",
		Capability: "integration.dns.record.inspect", Input: encoded, RequestContext: adapter.RequestContext{RequestID: "r", CorrelationID: "c", EnvironmentID: "test", Deadline: time.Now().Add(time.Minute)}}}
	if _, err := h.registry.Invoke(context.Background(), "integration.cloudflare-dns", request); err == nil {
		t.Fatal("invocation without an authorized token reference was accepted")
	}
	if len(h.fake.mutations) != 0 {
		t.Fatal("Cloudflare was changed")
	}
}

func TestBackendAddressUpsertCreatesReplacesAndConfirms(t *testing.T) {
	h := newHarness(t)
	response, output := h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)
	if got := h.fake.values("paper-main.backends.example.com", "A"); !reflect.DeepEqual(got, []string{"203.0.113.20/60/false"}) {
		t.Fatalf("records = %v", got)
	}

	// The instance's IP changed: the old value is removed after the new one
	// is created, and the TTL is applied.
	input := h.backendUpsert("203.0.113.99")
	input.TTLSeconds = 120
	response, output = h.mustInvoke("integration.dns.record.upsert", input)
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)
	if got := h.fake.values("paper-main.backends.example.com", "A"); !reflect.DeepEqual(got, []string{"203.0.113.99/120/false"}) {
		t.Fatalf("records = %v", got)
	}
	if !reflect.DeepEqual(h.fake.mutations, []string{"POST", "POST", "DELETE"}) {
		t.Fatalf("mutations = %v", h.fake.mutations)
	}

	// Repeating the same request changes nothing.
	response, output = h.mustInvoke("integration.dns.record.upsert", input)
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)
	if len(h.fake.mutations) != 3 {
		t.Fatalf("idempotent upsert changed records: %v", h.fake.mutations)
	}
}

func TestDirectJoinRequiresCurrentReadiness(t *testing.T) {
	cases := map[string]struct {
		mutate func(*upsertInput)
		reason string
	}{
		"expired":        {func(u *upsertInput) { u.ReadinessAssertion.ExpiresAt = testNow }, "readiness_assertion_expired"},
		"future":         {func(u *upsertInput) { u.ReadinessAssertion.ObservedAt = testNow.Add(time.Second) }, "readiness_assertion_not_yet_valid"},
		"other instance": {func(u *upsertInput) { u.ReadinessAssertion.InstanceRef.ExternalID = "instance-test-002" }, "readiness_assertion_mismatch"},
		"other server":   {func(u *upsertInput) { u.ReadinessAssertion.ServerID = "other" }, "readiness_assertion_mismatch"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			input := h.joinUpsert(testNow.Add(time.Minute))
			test.mutate(&input)
			response, output := h.mustInvoke("integration.dns.record.upsert", input)
			expect(t, response, output, "rejected", "absent", adapter.OutcomeCompleted)
			if output["reason_code"] != test.reason || len(h.fake.mutations) != 0 {
				t.Fatalf("reason %v, mutations %v", output["reason_code"], h.fake.mutations)
			}
		})
	}
	h := newHarness(t)
	response, output := h.mustInvoke("integration.dns.record.upsert", h.joinUpsert(testNow.Add(time.Minute)))
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)
}

func TestFallbackAndPurposeRules(t *testing.T) {
	h := newHarness(t)
	fallback := upsertInput{Record: h.target("play.example.com"), EnvironmentID: "test", Purpose: PurposeFallback, Values: []string{"203.0.113.5"}, TTLSeconds: 60}
	_, output := h.mustInvoke("integration.dns.record.upsert", fallback)
	if output["request_outcome"] != "rejected" || output["reason_code"] != "fallback_values_mismatch" {
		t.Fatalf("wrong fallback values: %v", output)
	}
	fallback.Values = []string{"198.51.100.10"}
	response, output := h.mustInvoke("integration.dns.record.upsert", fallback)
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)

	// The backend record does not allow direct joins.
	join := h.joinUpsert(testNow.Add(time.Minute))
	join.Record = h.target("paper-main.backends.example.com")
	_, output = h.mustInvoke("integration.dns.record.upsert", join)
	if output["reason_code"] != "purpose_not_allowed" {
		t.Fatalf("purpose: %v", output)
	}
	low := h.backendUpsert("203.0.113.20")
	low.TTLSeconds = 30
	_, output = h.mustInvoke("integration.dns.record.upsert", low)
	if output["reason_code"] != "ttl_below_provider_minimum" {
		t.Fatalf("ttl: %v", output)
	}
	if len(h.fake.mutations) != 1 {
		t.Fatalf("rejected requests changed records: %v", h.fake.mutations)
	}
}

func TestExpectedValuesAndProxiedRecords(t *testing.T) {
	h := newHarness(t)
	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.20", 60, false)
	input := h.backendUpsert("203.0.113.99")
	expected := []string{"203.0.113.1"}
	input.ExpectedValues = &expected
	_, output := h.mustInvoke("integration.dns.record.upsert", input)
	if output["reason_code"] != "current_values_mismatch" {
		t.Fatalf("expected values: %v", output)
	}
	expected = []string{}
	_, output = h.mustInvoke("integration.dns.record.upsert", input)
	if output["reason_code"] != "current_values_mismatch" {
		t.Fatalf("expected absent: %v", output)
	}
	expected = []string{"203.0.113.20"}
	response, output := h.mustInvoke("integration.dns.record.upsert", input)
	expect(t, response, output, "confirmed", "present", adapter.OutcomeCompleted)

	h = newHarness(t)
	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.20", 300, true)
	_, output = h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	if output["reason_code"] != "proxied_record_present" || len(h.fake.mutations) != 0 {
		t.Fatalf("proxied: %v %v", output, h.fake.mutations)
	}
}

func TestProviderFailuresAreClassified(t *testing.T) {
	// A refusal before any change is a rejection.
	h := newHarness(t)
	h.fake.failMutation, h.fake.failStatus = 1, http.StatusBadRequest
	response, output := h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	expect(t, response, output, "rejected", "absent", adapter.OutcomeCompleted)

	// A failure after a change may have left the record half-updated.
	h = newHarness(t)
	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.20", 60, false)
	h.fake.failMutation, h.fake.failStatus = 2, http.StatusBadRequest
	response, output = h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.99"))
	expect(t, response, output, "outcome_unknown", "unknown", adapter.OutcomeUnknown)

	// A 5xx on the first change is an unknown outcome too.
	h = newHarness(t)
	h.fake.failMutation, h.fake.failStatus = 1, http.StatusBadGateway
	response, output = h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	expect(t, response, output, "outcome_unknown", "unknown", adapter.OutcomeUnknown)

	// The provider accepted the writes but the read-back does not show them.
	h = newHarness(t)
	h.fake.ignoreWrites = true
	response, output = h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	expect(t, response, output, "accepted", "absent", adapter.OutcomeAccepted)

	// The record cannot be read before the change.
	h = newHarness(t)
	h.fake.listStatus = http.StatusServiceUnavailable
	_, output = h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	if output["request_outcome"] != "rejected" || output["reason_code"] != "provider_unavailable" || len(h.fake.mutations) != 0 {
		t.Fatalf("unreadable record: %v", output)
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	target := deleteInput{Record: h.target("paper-main.backends.example.com"), EnvironmentID: "test"}
	response, output := h.mustInvoke("integration.dns.record.delete", target)
	expect(t, response, output, "confirmed", "absent", adapter.OutcomeCompleted)

	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.20", 60, false)
	h.fake.add("paper-main.backends.example.com", "A", "203.0.113.21", 60, false)
	expected := []string{"203.0.113.20"}
	target.ExpectedValues = &expected
	_, output = h.mustInvoke("integration.dns.record.delete", target)
	if output["reason_code"] != "current_values_mismatch" || len(h.fake.mutations) != 0 {
		t.Fatalf("expected values: %v", output)
	}
	expected = []string{"203.0.113.21", "203.0.113.20"}
	response, output = h.mustInvoke("integration.dns.record.delete", target)
	expect(t, response, output, "confirmed", "absent", adapter.OutcomeCompleted)
	if got := h.fake.values("paper-main.backends.example.com", "A"); len(got) != 0 {
		t.Fatalf("records left: %v", got)
	}
}

func TestErrorsNeverIncludeTheToken(t *testing.T) {
	h := newHarness(t)
	h.fake.failMutation, h.fake.failStatus = 1, http.StatusBadGateway
	response, _ := h.mustInvoke("integration.dns.record.upsert", h.backendUpsert("203.0.113.20"))
	if strings.Contains(string(response.Result.Output), testToken) {
		t.Fatal("token leaked into output")
	}
	for _, err := range []error{errRefused, errUncertain} {
		if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "http") {
			t.Fatal("error message leaks request details")
		}
	}
}
