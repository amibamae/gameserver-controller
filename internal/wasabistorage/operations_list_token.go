package wasabistorage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// List continuation tokens: the provider token wrapped with an HMAC bound to
// this adapter instance, environment, container, and prefix.

type listTokenPayload struct {
	Environment string `json:"e"`
	Container   string `json:"c"`
	Prefix      string `json:"p"`
	Provider    string `json:"t"`
}

func (a *Adapter) sealListToken(providerToken, prefix string) string {
	payload, err := json.Marshal(listTokenPayload{Environment: a.config.EnvironmentID, Container: a.config.Container, Prefix: prefix, Provider: providerToken})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(a.listTokenMAC(payload))
}

func (a *Adapter) openListToken(token, prefix string) (string, bool) {
	payload, ok := a.verifiedListToken(token)
	if !ok {
		return "", false
	}
	var value listTokenPayload
	if json.Unmarshal(payload, &value) != nil || !a.boundListToken(value, prefix) {
		return "", false
	}
	return value.Provider, true
}

// verifiedListToken decodes a token and returns its payload when this
// adapter instance signed it.
func (a *Adapter) verifiedListToken(token string) ([]byte, bool) {
	encodedPayload, encodedMAC, found := strings.Cut(token, ".")
	if !found {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return nil, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return nil, false
	}
	return payload, hmac.Equal(signature, a.listTokenMAC(payload))
}

// boundListToken reports a payload for this environment, container, and
// prefix that carries a provider token.
func (a *Adapter) boundListToken(value listTokenPayload, prefix string) bool {
	return value.Environment == a.config.EnvironmentID && value.Container == a.config.Container && value.Prefix == prefix && value.Provider != ""
}

func (a *Adapter) listTokenMAC(payload []byte) []byte {
	mac := hmac.New(sha256.New, a.tokenKey[:])
	// A hash.Hash Write never returns an error.
	mac.Write(payload)
	return mac.Sum(nil)
}
