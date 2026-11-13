package vultrapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// These tests pin how the client classifies failures. A read that fails is a
// plain request failure the caller may retry; a mutation whose outcome cannot
// be known must never be reported as a definite failure, because a retry could
// create a second billable VM.

func TestReadFailuresAreRetryableRequestFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, ErrRequestFailed},
		{"provider rejection", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, ErrProviderReject},
		{"request timeout on a read", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusRequestTimeout) }, ErrProviderReject},
		{"oversized body", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"instance":{"id":"` + strings.Repeat("a", maximumResponseSize) + `"}}`))
		}, ErrInvalidResponse},
		{"response for another instance", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"instance":{"id":"other-instance"}}`))
		}, ErrInvalidResponse},
		{"undecodable body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not json`)) }, ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient(t, test.handler)
			_, err := client.GetInstance(context.Background(), []byte("test-token"), "instance-1")
			if !errors.Is(err, test.want) || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("GetInstance error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestReadTransportFailureIsARequestFailure(t *testing.T) {
	client, err := NewClientAt("https://api.example.invalid/v2", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset: secret-detail")
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetInstance(context.Background(), []byte("test-token"), "instance-1")
	if !errors.Is(err, ErrRequestFailed) || strings.Contains(err.Error(), "secret-detail") {
		t.Fatalf("GetInstance error = %v, want ErrRequestFailed without the transport detail", err)
	}
}

func TestMutationWithOversizedResponseIsUnknown(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(strings.Repeat(" ", maximumResponseSize+1)))
	})
	if _, err := client.CreateInstance(context.Background(), []byte("test-token"), testCreateInput()); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("CreateInstance error = %v, want ErrOutcomeUnknown", err)
	}
}

func TestRequestsWithoutAUsableCredentialOrContextAreRejected(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("request reached the provider") })
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func() error{
		"empty token": func() error { _, err := client.GetInstance(context.Background(), nil, "instance-1"); return err },
		"canceled":    func() error { _, err := client.GetInstance(canceled, []byte("test-token"), "instance-1"); return err },
		"invalid ID": func() error {
			_, err := client.GetInstance(context.Background(), []byte("test-token"), "../instances")
			return err
		},
		"invalid label": func() error {
			_, err := client.ListInstancesByLabel(context.Background(), []byte("test-token"), "")
			return err
		},
		"nil client call": func() error {
			_, err := (*Client)(nil).GetInstance(context.Background(), []byte("test-token"), "instance-1")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestListInstancesByLabelRejectsUnsafePages(t *testing.T) {
	page := func(instances, next string) string {
		return fmt.Sprintf(`{"instances":[%s],"meta":{"links":{"next":%s}}}`, instances, next)
	}
	tests := []struct {
		name string
		body func(call int) string
	}{
		{"undecodable page", func(int) string { return `not json` }},
		{"instance with an invalid ID", func(int) string { return page(`{"id":"../x","label":"gsc-label"}`, `""`) }},
		{"oversized cursor", func(call int) string { return page("", fmt.Sprintf(`"%d%s"`, call, strings.Repeat("c", 2049))) }},
		{"pages that never end", func(call int) string { return page("", fmt.Sprintf(`"cursor-%d"`, call)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := 0
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				call++
				_, _ = w.Write([]byte(test.body(call)))
			})
			if _, err := client.ListInstancesByLabel(context.Background(), []byte("test-token"), "gsc-label"); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("ListInstancesByLabel error = %v, want ErrInvalidResponse", err)
			}
			if test.name == "pages that never end" && call != maximumPages {
				t.Fatalf("followed %d pages, want the %d-page limit", call, maximumPages)
			}
			if test.name != "pages that never end" && call != 1 {
				t.Fatalf("requested %d pages, want the first page rejected", call)
			}
		})
	}
}
