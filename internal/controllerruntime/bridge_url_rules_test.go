package controllerruntime

import "testing"

// TestValidBridgeURLRules pins which Velocity bridge URLs are accepted.
func TestValidBridgeURLRules(t *testing.T) {
	for value, want := range map[string]bool{
		"https://bridge.example.com": true, "https://bridge.example.com/": true, "https://10.0.0.5:8443": true,
		"http://127.0.0.1:8080": true, "http://[::1]:8080": true, "http://LOCALHOST:8080": true,
		"http://bridge.example.com": false, "http://10.0.0.5": false, "ftp://127.0.0.1": false,
		"https://user@bridge.example.com": false, "https://bridge.example.com?x=1": false, "https://bridge.example.com#f": false,
		"https://bridge.example.com/api": false, "https://": false, "://bad": false, "": false,
	} {
		if got := validBridgeURL(value); got != want {
			t.Errorf("validBridgeURL(%q) = %t, want %t", value, got, want)
		}
	}
}
