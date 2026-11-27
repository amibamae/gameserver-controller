package velocityproxy

import "github.com/amibamae/gameserver-controller/internal/adapter"

// Descriptor advertises the proxy backend lifecycle, session inspection, and
// Velocity player transfer capabilities.
func (a *Adapter) Descriptor() adapter.Descriptor {
	schema := func(name string) string {
		return "urn:gameserver-controller:" + a.protocolVersion + ":integrations:" + name
	}
	capability := func(name, schemaName string, mutating bool, permission string) adapter.Capability {
		return adapter.Capability{
			Name: name, Version: capabilityVersion, Mutating: mutating,
			InputSchemaURI: schema(schemaName + "-input"), OutputSchemaURI: schema(schemaName + "-output"),
			RequiredPermissions: []string{permission},
		}
	}
	transfer := adapter.Capability{
		Name: "integration.velocity.players.transfer", Version: capabilityVersion, Mutating: true,
		InputSchemaURI: schema("velocity-player-transfer-request"), OutputSchemaURI: schema("velocity-player-transfer-result"),
		RequiredPermissions: []string{permissionBackendManage},
		Constraints: map[string]any{
			"max_sessions_per_invocation":                 maxSessionsPerTransfer,
			"requires_current_readiness_assertion":        true,
			"reconciliation_required_for_unknown_outcome": true,
		},
	}
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryIntegration, DisplayName: "Velocity proxy integration",
		ImplementationVersion: implementationVersion, ProtocolVersions: []string{a.protocolVersion},
		ContractVersions:    []string{contractVersion},
		RequiredPermissions: []string{permissionBackendManage, permissionSessionInspect},
		Capabilities: []adapter.Capability{
			capability("integration.proxy.backend.drain", "proxy-backend-drain", true, permissionBackendManage),
			capability("integration.proxy.backend.inspect", "proxy-backend-inspect", false, permissionBackendManage),
			capability("integration.proxy.backend.players.inspect", "proxy-backend-players-inspect", false, permissionBackendManage),
			capability("integration.proxy.backend.register", "proxy-backend-register", true, permissionBackendManage),
			capability("integration.proxy.backend.unregister", "proxy-backend-unregister", true, permissionBackendManage),
			capability("integration.proxy.cohort.inspect", "proxy-cohort-inspect", false, permissionSessionInspect),
			capability("integration.proxy.session.inspect", "proxy-session-inspect", false, permissionSessionInspect),
			transfer,
		},
		Constraints: map[string]any{"proxy_platform": "Velocity", "requires_proxy_session_identity": true},
	}
}
