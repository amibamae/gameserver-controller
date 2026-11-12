package vultrapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

const testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea"

func TestSSHKeyRegisterAndRead(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/ssh-keys":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ssh_key"] != testPublicKey+" gameserver-controller" || body["name"] != "gsc-pilot" {
				t.Fatalf("create body = %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"ssh_key": SSHKey{ID: "cb676a46-66fd-4dfb-b839-443f2e6c0b60", Name: body["name"], PublicKey: body["ssh_key"]}})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/ssh-keys/cb676a46-66fd-4dfb-b839-443f2e6c0b60":
			_ = json.NewEncoder(w).Encode(map[string]any{"ssh_key": SSHKey{ID: "cb676a46-66fd-4dfb-b839-443f2e6c0b60", Name: "gsc-pilot", PublicKey: testPublicKey + " gameserver-controller"}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	created, err := client.CreateSSHKey(context.Background(), []byte("synthetic-token"), "gsc-pilot", testPublicKey+" gameserver-controller")
	if err != nil || created.ID == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	read, err := client.GetSSHKey(context.Background(), []byte("synthetic-token"), created.ID)
	if err != nil || !strings.HasPrefix(read.PublicKey, testPublicKey) {
		t.Fatalf("get = %+v, %v", read, err)
	}
	if _, err := client.CreateSSHKey(context.Background(), []byte("synthetic-token"), "gsc", "ssh-ed25519 x\nssh-rsa y"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("multi-line key: %v", err)
	}
}

func TestReplaceInstanceUserDataConfirmsByReadingBack(t *testing.T) {
	stored := base64.StdEncoding.EncodeToString([]byte("#cloud-config\nssh_keys: {}\n"))
	ignorePatch := false
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v2/instances/instance-1":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !ignorePatch {
				stored = body["user_data"]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"instance": map[string]any{"id": "instance-1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/instances/instance-1/user-data":
			_ = json.NewEncoder(w).Encode(map[string]any{"user_data": map[string]any{"data": stored}})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	if err := client.ReplaceInstanceUserData(context.Background(), []byte("synthetic-token"), "instance-1", []byte("#cloud-config\n{}\n")); err != nil {
		t.Fatal(err)
	}
	ignorePatch = true
	stored = base64.StdEncoding.EncodeToString([]byte("old"))
	if err := client.ReplaceInstanceUserData(context.Background(), []byte("synthetic-token"), "instance-1", []byte("#cloud-config\n{}\n")); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("a replacement the provider ignored was reported as done: %v", err)
	}
}

func TestFirewallMustAdmitSSHOnlyFromTheController(t *testing.T) {
	controller := netip.MustParseAddr("203.0.113.10")
	ssh := func(subnet string, size int) FirewallRule {
		return FirewallRule{ID: 1, IPType: "v4", Action: "accept", Protocol: "tcp", Port: "22", Subnet: subnet, SubnetSize: size}
	}
	game := FirewallRule{ID: 2, IPType: "v4", Action: "accept", Protocol: "tcp", Port: "25565", Subnet: "0.0.0.0", SubnetSize: 0}
	cases := map[string]struct {
		rules []FirewallRule
		ok    bool
	}{
		"exact controller":         {[]FirewallRule{ssh("203.0.113.10", 32), game}, true},
		"open to all":              {[]FirewallRule{ssh("0.0.0.0", 0)}, false},
		"controller's /24":         {[]FirewallRule{ssh("203.0.113.0", 24)}, false},
		"another address":          {[]FirewallRule{ssh("198.51.100.1", 32)}, false},
		"range covering 22":        {[]FirewallRule{ssh("203.0.113.10", 32), {ID: 3, Action: "accept", Protocol: "tcp", Port: "1:1024", Subnet: "0.0.0.0"}}, false},
		"all ports":                {[]FirewallRule{ssh("203.0.113.10", 32), {ID: 4, Action: "accept", Protocol: "tcp", Port: "", Subnet: "0.0.0.0"}}, false},
		"cloudflare source":        {[]FirewallRule{{ID: 5, Action: "accept", Protocol: "tcp", Port: "22", Source: "cloudflare"}}, false},
		"unknown protocol":         {[]FirewallRule{ssh("203.0.113.10", 32), {ID: 6, Action: "accept", Protocol: "any", Subnet: "0.0.0.0"}}, false},
		"no SSH rule":              {[]FirewallRule{game}, false},
		"udp 22 is not SSH":        {[]FirewallRule{ssh("203.0.113.10", 32), {ID: 7, Action: "accept", Protocol: "udp", Port: "22", Subnet: "0.0.0.0"}}, true},
		"ipv6 open while v4 exact": {[]FirewallRule{ssh("203.0.113.10", 32), {ID: 8, IPType: "v6", Action: "accept", Protocol: "tcp", Port: "22", Subnet: "::", SubnetSize: 0}}, false},
	}
	for name, test := range cases {
		if err := SSHOnlyFrom(test.rules, controller); (err == nil) != test.ok {
			t.Errorf("%s: SSHOnlyFrom = %v", name, err)
		}
	}
}

func TestListFirewallRulesFollowsPages(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/firewalls/fw-1/rules" {
			t.Fatalf("unexpected %s", r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"firewall_rules": []FirewallRule{{ID: 1, Protocol: "tcp", Port: "22", Subnet: "203.0.113.10", SubnetSize: 32}},
				"meta": map[string]any{"total": 2, "links": map[string]any{"next": "page2"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"firewall_rules": []FirewallRule{{ID: 2, Protocol: "tcp", Port: "25565", Subnet: "0.0.0.0"}},
			"meta": map[string]any{"total": 2, "links": map[string]any{"next": ""}}})
	})
	rules, err := client.ListFirewallRules(context.Background(), []byte("synthetic-token"), "fw-1")
	if err != nil || len(rules) != 2 {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
}
