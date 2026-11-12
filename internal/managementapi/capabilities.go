package managementapi

import "net/http"

// capabilities serves GET /v1/capabilities to any authenticated key: the
// protocol version and the registered adapter descriptors.
func (a *api) capabilities(w http.ResponseWriter, r *http.Request) {
	if _, ok := principalOf(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"protocol_versions": []string{a.config.ProtocolVersion},
		"capabilities":      a.config.Controller.Descriptors(),
	})
}
