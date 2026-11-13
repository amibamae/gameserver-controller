package vultrapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests pin the exact error for every request rule and HTTP status
// class, so the checks can be reorganized without changing what callers see.

func TestValidateCreateInstanceRequestReportsEachRule(t *testing.T) {
	identity := "invalid Vultr API request: region, plan, os_id, label, or hostname is invalid"
	manyIDs := make([]string, 101)
	manyTags := make([]string, 101)
	for i := range manyIDs {
		manyIDs[i] = "key-" + strings.Repeat("a", i%5+1)
		manyTags[i] = "tag-" + strings.Repeat("b", i+1)
	}
	cases := []struct {
		name   string
		mutate func(*CreateInstanceRequest)
		want   string
	}{
		{"valid", func(*CreateInstanceRequest) {}, ""},
		{"no optional fields", func(input *CreateInstanceRequest) {
			input.Hostname, input.StartupScriptID, input.FirewallGroupID, input.UserData = "", "", "", ""
			input.SSHKeyIDs, input.Tags = nil, nil
		}, ""},
		{"blank region", func(input *CreateInstanceRequest) { input.Region = "" }, identity},
		{"long plan", func(input *CreateInstanceRequest) { input.Plan = strings.Repeat("p", 201) }, identity},
		{"negative os id", func(input *CreateInstanceRequest) { input.OSID = -1 }, identity},
		{"control character label", func(input *CreateInstanceRequest) { input.Label = "a\x7fb" }, identity},
		{"hostname with space", func(input *CreateInstanceRequest) { input.Hostname = "a b" }, identity},
		{"long hostname", func(input *CreateInstanceRequest) { input.Hostname = strings.Repeat("h", 254) }, identity},
		{"hostname with control character", func(input *CreateInstanceRequest) { input.Hostname = "a\x01b" }, identity},
		{"startup script", func(input *CreateInstanceRequest) { input.StartupScriptID = "a/b" }, "invalid Vultr API request: invalid startup script ID"},
		{"firewall group", func(input *CreateInstanceRequest) { input.FirewallGroupID = "a b" }, "invalid Vultr API request: invalid firewall group ID"},
		{"too many SSH keys", func(input *CreateInstanceRequest) { input.SSHKeyIDs = manyIDs }, "invalid Vultr API request: too many SSH key IDs"},
		{"SSH key", func(input *CreateInstanceRequest) { input.SSHKeyIDs = []string{"ok", ""} }, "invalid Vultr API request: invalid SSH key ID"},
		{"user data not base64", func(input *CreateInstanceRequest) { input.UserData = "%%" }, "invalid Vultr API request: user_data must be base64-encoded and at most 64 KiB"},
		{"user data too large", func(input *CreateInstanceRequest) {
			input.UserData = base64.StdEncoding.EncodeToString(make([]byte, 64<<10+1))
		}, "invalid Vultr API request: user_data must be base64-encoded and at most 64 KiB"},
		{"too many tags", func(input *CreateInstanceRequest) { input.Tags = manyTags }, "invalid Vultr API request: too many tags"},
		{"blank tag", func(input *CreateInstanceRequest) { input.Tags = []string{" "} }, "invalid Vultr API request: invalid or duplicate tag"},
		{"first failing rule wins", func(input *CreateInstanceRequest) {
			input.FirewallGroupID = "a b"
			input.Tags = []string{"x", "x"}
		}, "invalid Vultr API request: invalid firewall group ID"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := testCreateInput()
			test.mutate(&input)
			got := ""
			if err := ValidateCreateInstanceRequest(input); err != nil {
				got = err.Error()
			}
			if got != test.want {
				t.Fatalf("error = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRequestStatusClassification(t *testing.T) {
	cases := []struct {
		status     int
		read, post string
	}{
		{http.StatusOK, "", ""},
		{http.StatusFound, "Vultr API request failed: HTTP 302", "Vultr create outcome is unknown"},
		{http.StatusBadRequest, "Vultr rejected the request: HTTP 400", "Vultr rejected the request: HTTP 400"},
		{http.StatusRequestTimeout, "Vultr rejected the request: HTTP 408", "Vultr create outcome is unknown"},
		{http.StatusTooManyRequests, "Vultr rejected the request: HTTP 429", "Vultr rejected the request: HTTP 429"},
		{http.StatusInternalServerError, "Vultr API request failed: HTTP 500", "Vultr create outcome is unknown"},
		{199, "Vultr API request failed: HTTP 199", "Vultr create outcome is unknown"},
	}
	for _, test := range cases {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"instance":{"id":"instance-1"}}`))
			})
			_, err := client.GetInstance(context.Background(), []byte(testToken), "instance-1")
			if got := errorText(err); got != test.read {
				t.Errorf("read error = %q, want %q", got, test.read)
			}
			_, err = client.CreateInstance(context.Background(), []byte(testToken), testCreateInput())
			if got := errorText(err); got != test.post {
				t.Errorf("create error = %q, want %q", got, test.post)
			}
		})
	}
}

func TestRequestRejectsAMissingClientOrBadToken(t *testing.T) {
	want := "invalid Vultr API request: context, client, and API credential are required"
	var missing *Client
	if _, err := missing.GetInstance(context.Background(), []byte(testToken), "instance-1"); errorText(err) != want {
		t.Fatalf("nil client error = %v", err)
	}
	client := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("request reached the provider") })
	for _, token := range []string{" padded", "a\x00b", strings.Repeat("t", 4097)} {
		if _, err := client.GetInstance(context.Background(), []byte(token), "instance-1"); errorText(err) != want {
			t.Fatalf("token %q error = %v", token, err)
		}
	}
}

func TestNewClientAtNormalizesPathAndTimeout(t *testing.T) {
	for _, base := range []string{"https://api.example.invalid", "https://api.example.invalid/", "https://api.example.invalid/v2"} {
		client, err := NewClientAt(base, nil)
		if err != nil {
			t.Fatalf("NewClientAt(%q): %v", base, err)
		}
		if client.baseURL.String() != "https://api.example.invalid/v2" || client.http.Timeout != 30*time.Second || client.http.CheckRedirect == nil {
			t.Fatalf("NewClientAt(%q) = %s timeout %v", base, client.baseURL, client.http.Timeout)
		}
	}
	client, err := NewClientAt("https://api.example.invalid/v2", &http.Client{Timeout: time.Second})
	if err != nil || client.http.Timeout != time.Second {
		t.Fatalf("caller timeout not kept: %v %v", client, err)
	}
	for base, want := range map[string]string{
		"https://api.example.invalid/v2#frag": "invalid Vultr API request: API base URL must be an HTTPS origin",
		"https:///v2":                         "invalid Vultr API request: API base URL must be an HTTPS origin",
		"://bad":                              "invalid Vultr API request: API base URL must be an HTTPS origin",
		"https://api.example.invalid/v2/x":    "invalid Vultr API request: API base URL path must be /v2",
	} {
		if _, err := NewClientAt(base, nil); errorText(err) != want {
			t.Errorf("NewClientAt(%q) error = %v, want %q", base, err, want)
		}
	}
	if production := NewClient(nil); production.baseURL.String() != "https://api.vultr.com/v2" {
		t.Fatalf("NewClient base URL = %s", production.baseURL)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
