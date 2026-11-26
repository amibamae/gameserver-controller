package fakeadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

var ProxyCounter int64

// VelocityProxySimulator simulates a Velocity proxy for testing.
type VelocityProxySimulator struct {
	proxyCount    int64
	proxies       map[string]*VelocityProxyState
	registeredServers []ServerRegistration
	drainCount    atomic.Int32
}

// ServerRegistration tracks a registered Minecraft server for the proxy.
type ServerRegistration struct {
	ID            string
	Host          string
	Port          int
	Version       string
	State         string // ready, draining, unregistered
	Type          string // internal, external
	PlayerTransfer bool
}

// VelocityProxyState tracks the state of a Velocity proxy instance.
type VelocityProxyState struct {
	ID             string
	Name           string
	Host           string
	Port           int
	Backend        string
	State          string // running, stopped
	Registered     []ServerRegistration
	Draining       bool
	TotalServers   int64
	Transferred    int64
}

// NewVelocityProxySimulator creates a new Velocity proxy simulator.
func NewVelocityProxySimulator() *VelocityProxySimulator {
	return &VelocityProxySimulator{
		proxies: make(map[string]*VelocityProxyState),
	}
}

// Capabilities returns advertised capabilities.
func (v *VelocityProxySimulator) Capabilities() []string {
	return []string{"proxy.register", "proxy.unregister", "proxy.drain", "proxy.list"}
}

// Register simulates registering a backend server with the proxy.
func (v *VelocityProxySimulator) Register(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	
	name := ""
	if nameVal, ok := input["name"].(string); ok {
		name = nameVal
	}

	host := "127.0.0.1"
	if hostVal, ok := input["host"].(string); ok {
		host = hostVal
	}

	port := 25576
	if portVal, ok := input["port"].(float64); ok {
		port = int(portVal)
	}

	version := "1.20.x"
	if versionVal, ok := input["version"].(string); ok {
		version = versionVal
	}

	serverID := fmt.Sprintf("server-%d", v.proxyCount.Load()&65535)

	reg := ServerRegistration{
		ID:       serverID,
		Host:     host,
		Port:     port,
		Version:  version,
		Type:     "internal",
		State:    "ready",
	}

	if _, exists := v.proxies[serverID]; !exists {
		v.totalServers = int64(v.totalServers + 1)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"status": "success",
		"id":     serverID,
		"name":   name,
	})
	return json.RawMessage(data), nil
}

// Unregister simulates unregistering a backend server from the proxy.
func (v *VelocityProxySimulator) Unregister(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		allServers := make([]map[string]interface{}, 0)
		for _, reg := range v.registeredServers {
			allServers = append(allServers, map[string]interface{}{
				"id":      reg.ID,
				"host":    reg.Host,
				"port":    reg.Port,
				"version": reg.Version,
				"type":    reg.Type,
			})
		}
		return json.RawMessage(fmt.Sprintf(`{"status": "success", "servers": %s}`, json.Marshal(allServers))), nil
	}

	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	v.registeredServers = removeMatching(v.registeredServers, func(r ServerRegistration) bool {
		return r.ID == id
	})

	data, _ := json.Marshal(map[string]interface{}{
		"status": "success",
		"id":     id,
	})
	return json.RawMessage(data), nil
}

// Drain simulates initiating drain of a backend server.
func (v *VelocityProxySimulator) Drain(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		return json.RawMessage(`{"status": "error", "message": "server id required"}`), nil
	}

	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	v.drainCount.Add(1)

	data, _ := json.Marshal(map[string]interface{}{
		"status": "success",
		"id":     id,
	})
	return json.RawMessage(data), nil
}

// List simulates listing all registered proxy backends.
func (v *VelocityProxySimulator) List(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	backends := make([]map[string]interface{}, 0)
	for _, serverReg := range v.registeredServers {
		backends = append(backends, map[string]interface{}{
			"id":       serverReg.ID,
			"host":     serverReg.Host,
			"port":     serverReg.Port,
			"version":  serverReg.Version,
			"type":     serverReg.Type,
			"state":    serverReg.State,
		})
	}

	data, _ := json.Marshal(map[string]interface{}{
		"servers": backends,
	})
	return json.RawMessage(data), nil
}

func removeMatching[T any](slice []T, match func(T) bool) []T {
	result := make([]T, 0, len(slice))
	for _, item := range slice {
		if !match(item) {
			result = append(result, item)
		}
	}
	return result
}
