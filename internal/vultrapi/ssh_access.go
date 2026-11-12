package vultrapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// SSHKey is a public key registered with the Vultr account. Vultr installs
// the keys named at create in the new instance's root authorized_keys.
type SSHKey struct {
	ID          string `json:"id"`
	DateCreated string `json:"date_created,omitempty"`
	Name        string `json:"name"`
	PublicKey   string `json:"ssh_key"`
}

type sshKeyEnvelope struct {
	SSHKey SSHKey `json:"ssh_key"`
}

// GetSSHKey reads one exact SSH key ID.
func (c *Client) GetSSHKey(ctx context.Context, token []byte, keyID string) (SSHKey, error) {
	if !validResourceID(keyID) {
		return SSHKey{}, fmt.Errorf("%w: invalid SSH key ID", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, http.MethodGet, "ssh-keys/"+keyID, nil, nil, false)
	if err != nil {
		return SSHKey{}, err
	}
	var envelope sshKeyEnvelope
	if decodeJSON(response, &envelope) != nil || envelope.SSHKey.ID != keyID || !validPublicKeyField(envelope.SSHKey.PublicKey) {
		return SSHKey{}, ErrInvalidResponse
	}
	return envelope.SSHKey, nil
}

// CreateSSHKey registers one public key. It sends one POST and never retries;
// on ErrOutcomeUnknown, check the account's keys before trying again.
func (c *Client) CreateSSHKey(ctx context.Context, token []byte, name, publicKey string) (SSHKey, error) {
	if !validShortField(name, 255) || !validPublicKeyField(publicKey) {
		return SSHKey{}, fmt.Errorf("%w: SSH key name or public key is invalid", ErrInvalidRequest)
	}
	body, err := json.Marshal(map[string]string{"name": name, "ssh_key": publicKey})
	if err != nil {
		return SSHKey{}, fmt.Errorf("%w: could not encode SSH key request", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, http.MethodPost, "ssh-keys", nil, body, true)
	if err != nil {
		return SSHKey{}, err
	}
	var envelope sshKeyEnvelope
	if decodeJSON(response, &envelope) != nil || !validResourceID(envelope.SSHKey.ID) || envelope.SSHKey.Name != name ||
		!sameKeyMaterial(envelope.SSHKey.PublicKey, publicKey) {
		return SSHKey{}, ErrOutcomeUnknown
	}
	return envelope.SSHKey, nil
}

func validPublicKeyField(value string) bool {
	return value != "" && len(value) <= 16<<10 && !strings.ContainsAny(value, "\r\n\x00")
}

// sameKeyMaterial compares the type and base64 fields, ignoring comments.
func sameKeyMaterial(left, right string) bool {
	a, b := strings.Fields(left), strings.Fields(right)
	return len(a) >= 2 && len(b) >= 2 && a[0] == b[0] && a[1] == b[1]
}

// ReplaceInstanceUserData overwrites an instance's stored user data with the
// given plaintext, then reads it back. It returns nil only when the provider
// now reports exactly that value.
func (c *Client) ReplaceInstanceUserData(ctx context.Context, token []byte, instanceID string, userData []byte) error {
	if !validResourceID(instanceID) || len(userData) == 0 || len(userData) > 64<<10 {
		return fmt.Errorf("%w: invalid instance ID or user data", ErrInvalidRequest)
	}
	encoded := base64.StdEncoding.EncodeToString(userData)
	body, err := json.Marshal(map[string]string{"user_data": encoded})
	if err != nil {
		return fmt.Errorf("%w: could not encode user data", ErrInvalidRequest)
	}
	if _, err := c.request(ctx, token, http.MethodPatch, "instances/"+instanceID, nil, body, true); err != nil {
		return err
	}
	response, err := c.request(ctx, token, http.MethodGet, "instances/"+instanceID+"/user-data", nil, nil, false)
	if err != nil {
		return ErrOutcomeUnknown
	}
	var envelope struct {
		UserData struct {
			Data *string `json:"data"`
		} `json:"user_data"`
	}
	if decodeJSON(response, &envelope) != nil || envelope.UserData.Data == nil || *envelope.UserData.Data != encoded {
		return ErrOutcomeUnknown
	}
	return nil
}

// FirewallRule is one inbound rule of a Vultr firewall group. Vultr drops
// inbound traffic that no accept rule matches.
type FirewallRule struct {
	ID         int    `json:"id"`
	IPType     string `json:"ip_type"`
	Action     string `json:"action"`
	Protocol   string `json:"protocol"`
	Port       string `json:"port"`
	Subnet     string `json:"subnet"`
	SubnetSize int    `json:"subnet_size"`
	Source     string `json:"source"`
}

// ListFirewallRules reads every rule of one firewall group. A partial or
// inconsistent listing is an error, never a shorter success.
func (c *Client) ListFirewallRules(ctx context.Context, token []byte, groupID string) ([]FirewallRule, error) {
	if !validResourceID(groupID) {
		return nil, fmt.Errorf("%w: invalid firewall group ID", ErrInvalidRequest)
	}
	var rules []FirewallRule
	cursors := map[string]bool{}
	cursor, total := "", -1
	for page := 0; page < maximumPages; page++ {
		query := url.Values{"per_page": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := c.request(ctx, token, http.MethodGet, "firewalls/"+groupID+"/rules", query, nil, false)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Rules []FirewallRule    `json:"firewall_rules"`
			Meta  *instanceListMeta `json:"meta"`
		}
		if decodeJSON(response, &envelope) != nil || envelope.Rules == nil || envelope.Meta == nil || envelope.Meta.Total == nil ||
			envelope.Meta.Links == nil || envelope.Meta.Links.Next == nil {
			return nil, ErrInvalidResponse
		}
		if total == -1 {
			total = *envelope.Meta.Total
		}
		if total < 0 || total > maximumPages*100 || total != *envelope.Meta.Total || len(envelope.Rules) > 100 {
			return nil, ErrInvalidResponse
		}
		rules = append(rules, envelope.Rules...)
		cursor = *envelope.Meta.Links.Next
		if cursor == "" {
			if len(rules) != total {
				return nil, ErrInvalidResponse
			}
			return rules, nil
		}
		if len(envelope.Rules) == 0 || len(rules) >= total || len(cursor) > 2048 || cursors[cursor] {
			return nil, ErrInvalidResponse
		}
		cursors[cursor] = true
	}
	return nil, ErrInvalidResponse
}

// SSHOnlyFrom reports whether a firewall group admits TCP port 22 only from
// the controller's exact address: every accept rule that can match SSH must
// name that address with a full-length prefix and no provider source list.
// At least one such rule must exist, or the controller is locked out.
func SSHOnlyFrom(rules []FirewallRule, controller netip.Addr) error {
	if !controller.IsValid() {
		return fmt.Errorf("%w: controller address is invalid", ErrInvalidRequest)
	}
	controller = controller.Unmap()
	allowed := false
	for _, rule := range rules {
		if rule.Action != "" && rule.Action != "accept" {
			continue
		}
		if !coversSSH(rule) {
			continue
		}
		subnet, err := netip.ParseAddr(rule.Subnet)
		full := 32
		if subnet.Is6() && !subnet.Is4In6() {
			full = 128
		}
		if err != nil || rule.Source != "" || rule.SubnetSize != full || subnet.Unmap() != controller {
			return fmt.Errorf("firewall rule %d admits SSH from %s/%d, not only the controller", rule.ID, rule.Subnet, rule.SubnetSize)
		}
		allowed = true
	}
	if !allowed {
		return fmt.Errorf("the firewall group has no rule admitting SSH from the controller")
	}
	return nil
}

// coversSSH reports whether a rule can match TCP port 22. An unknown
// protocol or port form is treated as matching, so it fails closed.
func coversSSH(rule FirewallRule) bool {
	switch strings.ToLower(rule.Protocol) {
	case "tcp":
	case "udp", "icmp", "gre", "esp", "ah":
		return false
	default:
		return true
	}
	port := strings.TrimSpace(rule.Port)
	if port == "" {
		return true
	}
	low, high, isRange := strings.Cut(port, ":")
	if !isRange {
		high = low
	}
	from, err1 := strconv.Atoi(low)
	to, err2 := strconv.Atoi(high)
	if err1 != nil || err2 != nil {
		return true
	}
	return from <= 22 && 22 <= to
}
