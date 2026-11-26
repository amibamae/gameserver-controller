package fakeadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

var ServerCounter int64

// PaperMCServerSimulator simulates a PaperMC server for testing.
type PaperMCServerSimulator struct {
	serverCount int64
	servers     map[string]*PaperMCInstanceState
	startCount  atomic.Int32
	stopCount   atomic.Int32
}

// PaperMCInstanceState tracks a simulated PaperMC server's state.
type PaperMCInstanceState struct {
	ID           string
	Name         string
	Host         string
	Port         int
	Version      string
	State        string // running, stopped, starting, stopping
	JARPath      string
	Uptime       int64 // Unix timestamp when started
	MemoryLimit  int64  // MB
	MemoryUsed   int64  // MB
	Players      []*PaperMCPlayerState
	Ticks        int64
	Worlds       []string
}

// PaperMCPlayerState tracks simulated player connections.
type PaperMCPlayerState struct {
	Name          string
	XPSession     string
	IsCreative    bool
	LastPosition  [3]float64
	Gamemode      string
	DeathCount    int32
	CreatedAt     int64
}

// DefaultPaperMCConfig provides default configuration.
type PaperMCDefaultConfig struct {
	DefaultPort int
	DefaultMemory int64 // MB
}

// NewPaperMCServerSimulator creates a new PaperMC server simulator.
func NewPaperMCServerSimulator() *PaperMCServerSimulator {
	return &PaperMCServerSimulator{
		servers: make(map[string]*PaperMCInstanceState),
	}
}

// Capabilities returns advertised capabilities.
func (p *PaperMCServerSimulator) Capabilities() []string {
	return []string{"server.start", "server.stop", "server.info", "server.list"}
}

// Start simulates starting a PaperMC server.
func (p *PaperMCServerSimulator) Start(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	
	name := ""
	if nameVal, ok := input["name"].(string); ok {
		name = nameVal
	}

	version := "1.20.x"
	if versionVal, ok := input["version"].(string); ok {
		version = versionVal
	}

	memoryLimit := 4096 // MB default
	if memoryVal, ok := input["memory_limit"].(float64); ok {
		memoryLimit = int64(memoryVal)
	}

	jarPath := ""
	if jarVal, ok := input["jar_path"].(string); ok {
		jarPath = jarVal
	} else {
		jarPath = "paper.jar"
	}

	port := 25565
	if portVal, ok := input["port"].(float64); ok {
		port = int(portVal)
	}

	id := fmt.Sprintf("paper-%d", atomic.AddInt64(&p.serverCount, 1))
	serverID := fmt.Sprintf("server-%d", p.startCount.Add(1))

	server := &PaperMCInstanceState{
		ID:       id,
		Name:     name,
		Host:     "127.0.0.1",
		Port:     port,
		Version:  version,
		State:    "running",
		JARPath:  jarPath,
		Uptime:   time.Now().Unix(),
		MemoryLimit: memoryLimit,
		MemoryUsed: int64(float32(memoryLimit) * 0.5), // Simulated 50% usage
		Ticks:    0,
		Worlds: []string{
			fmt.Sprintf("world-%d", p.startCount.Load()),
		},
	}

	data, _ := json.Marshal(server)
	return json.RawMessage(data), nil
}

// Stop simulates stopping a PaperMC server.
func (p *PaperMCServerSimulator) Stop(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		// Return success for all servers
		return json.RawMessage(`{"status": "success", "message": "all servers stopped"}`), nil
	}

	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	if len(id) > 0 {
		server, exists := p.servers[id]
		if !exists {
			return json.RawMessage(`{"status": "error", "message": "server not found"}`), nil
		}
		
		data, _ := json.Marshal(map[string]interface{}{
			"status": "success",
			"id":     id,
			"name":   server.Name,
		})
		return json.RawMessage(data), nil
	}

	return json.RawMessage(`{"status": "error", "message": "server id required"}`), nil
}

// Info simulates getting server info.
func (p *PaperMCServerSimulator) Info(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		return json.RawMessage(`{"status": "error", "message": "server id required"}`), nil
	}

	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	server, exists := p.servers[id]
	if !exists {
		return json.RawMessage(`{"status": "error", "message": "server not found"}`), nil
	}

	data, _ := json.Marshal(server)
	return json.RawMessage(data), nil
}

// List simulates listing all servers.
func (p *PaperMCServerSimulator) List(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	servers := make([]*PaperMCInstanceState, 0)
	for _, server := range p.servers {
		servers = append(servers, server)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"servers": servers,
	})
	return json.RawMessage(data), nil
}
