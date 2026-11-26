
// Invoke implements the adapter interface for all Vultr hosting capabilities.
func (a *fakeVultrAdapter) Invoke(ctx context.Context, schemaURI string, body json.RawMessage) (json.RawMessage, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err == nil {
		uuid, _ := req["instanceUuid"].(string)
	}
	
	switch {
	case strings.Contains(schemaURI, "region"):
		resp := map[string]any{
			"data": []map[string]any{
				{"uuid": "fra1", "name": "Frankfurt", "flag": "DE"},
				{"uuid": "lon1", "name": "London", "flag": "GB"},
				{"uuid": "nyc3", "name": "New York", "flag": "US"},
			},
		}
		return json.Marshal(resp)
	
	case strings.Contains(schemaURI, "plan"):
		resp := map[string]any{
			"data": []map[string]any{
				{"uuid": "c2-small", "name": "C2 Small", "cpu": 1, "ram_mb": 1024, "disk_gb": 25},
				{"uuid": "c2-medium", "name": "C2 Medium", "cpu": 2, "ram_mb": 2048, "disk_gb": 50},
			},
		}
		return json.Marshal(resp)
	
	case strings.Contains(schemaURI, "os"):
		resp := map[string]any{
			"data": []map[string]any{
				{"uuid": "ubuntu-22.04", "name": "Ubuntu 22.04 LTS", "family": "debian"},
				{"uuid": "ubuntu-20.04", "name": "Ubuntu 20.04 LTS", "family": "debian"},
			},
		}
		return json.Marshal(resp)
	
	case strings.Contains(schemaURI, "create"):
		var createReq struct {
			Plan       string `json:"plan"`
			OS         string `json:"os"`
			Region     string `json:"region"`
			Name       string `json:"name"`
			RootPassword string
		}
		if json.Unmarshal(body, &createReq) == nil {
			uuid := a.nextUUID
			a.nextUUID = fmt.Sprintf("test-%d", time.Now().UnixNano())
			
			inst := fakeInstance{
				UUID:        uuid,
				Name:        createReq.Name,
				Hostname:    fmt.Sprintf("%s.%s.internal", createReq.Name, createReq.Region),
				Region:      createReq.Region,
				Plan:        createReq.Plan,
				OS:          createReq.OS,
				Status:      "initializing",
				CreatedAt:   time.Now(),
				Tags:        []string{},
			}
			a.instances[uuid] = inst
			
			return json.Marshal(map[string]any{
				"data": map[string]any{
					"type":    "instance",
					"uuid":    uuid,
					"name":    inst.Name,
					"hostname": inst.Hostname,
					"status":  inst.Status,
				},
			})
		}
	
	case strings.Contains(schemaURI, "get"):
		var getReq struct { InstanceUuid string `json:"instanceUuid"` }
		if json.Unmarshal(body, &getReq) == nil && getReq.InstanceUuid != "" {
			if inst, exists := a.instances[getReq.InstanceUuid]; exists {
				return json.Marshal(map[string]any{
					"data": map[string]any{
						"type":   "instance",
						"uuid":   inst.UUID,
						"name":   inst.Name,
						"status": inst.Status,
					},
				})
			}
			return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("instance not found")}
		}
	
	case strings.Contains(schemaURI, "delete"):
		var deleteReq struct { InstanceUuid string `json:"instanceUuid"` }
		if json.Unmarshal(body, &deleteReq) == nil && deleteReq.InstanceUuid != "" {
			delete(a.instances, deleteReq.InstanceUuid)
			return json.Marshal(map[string]any{"data": map[string]any{"type": "success"}})
		}
	
	case strings.Contains(schemaURI, "start"):
		var startReq struct { InstanceUuid string `json:"instanceUuid"` }
		if json.Unmarshal(body, &startReq) == nil && startReq.InstanceUuid != "" {
			if inst, exists := a.instances[startReq.InstanceUuid]; exists {
				inst.Status = "active"
				return json.Marshal(map[string]any{"data": map[string]any{"type": "success", "status": inst.Status}})
			}
			return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("instance not found")}
		}
	
	case strings.Contains(schemaURI, "stop"):
		var stopReq struct { InstanceUuid string `json:"instanceUuid"` }
		if json.Unmarshal(body, &stopReq) == nil && stopReq.InstanceUuid != "" {
			if inst, exists := a.instances[stopReq.InstanceUuid]; exists {
				inst.Status = "stopped"
				return json.Marshal(map[string]any{"data": map[string]any{"type": "success", "status": inst.Status}})
			}
			return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("instance not found")}
		}
	
	case strings.Contains(schemaURI, "reboot"):
		var rebootReq struct { InstanceUuid string `json:"instanceUuid"` }
		if json.Unmarshal(body, &rebootReq) == nil && rebootReq.InstanceUuid != "" {
			if inst, exists := a.instances[rebootReq.InstanceUuid]; exists {
				inst.Status = "active"
				return json.Marshal(map[string]any{"data": map[string]any{"type": "success", "status": inst.Status}})
			}
			return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("instance not found")}
		}
	
	case strings.Contains(schemaURI, "list"):
		var instances []fakeInstance
		for _, inst := range a.instances {
			instances = append(instances, inst)
		}
		return json.Marshal(map[string]any{
			"data": map[string]any{"type": "instance", "items": instances},
		})
	}
	
	return nil, fmt.Errorf("unknown operation")
}
