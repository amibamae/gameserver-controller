package vultrapi

import (
	"context"
	"errors"
	"net/netip"
)

// ErrIPNotAssigned reports that Vultr has not assigned the instance a public
// IP yet. Vultr returns 0.0.0.0 as main_ip while an instance is pending.
var ErrIPNotAssigned = errors.New("Vultr instance has no public IP yet")

// InstanceMainIP returns the current public IPv4 address of one exact
// instance. Callers that route traffic to the instance should call it again
// before each routing change, because the address can change if the
// instance is rebuilt.
func (c *Client) InstanceMainIP(ctx context.Context, token []byte, instanceID string) (netip.Addr, error) {
	instance, err := c.GetInstance(ctx, token, instanceID)
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(instance.MainIP)
	if err != nil || !ip.Is4() {
		if instance.MainIP == "" {
			return netip.Addr{}, ErrIPNotAssigned
		}
		return netip.Addr{}, ErrInvalidResponse
	}
	if ip.IsUnspecified() {
		return netip.Addr{}, ErrIPNotAssigned
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
		return netip.Addr{}, ErrInvalidResponse
	}
	return ip, nil
}
