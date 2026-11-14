package pilotvultr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

func (p *Provisioner) prepare(request CreateRequest) (vultrapi.CreateInstanceRequest, store.CapacityReservation, error) {
	if !operationIDPattern.MatchString(request.OperationID) || request.ServerID == "" || request.Environment == "" {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	groupID, _, ok := p.policy.GroupForEnvironment(request.Environment)
	if !ok {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, pilotcapacity.ErrUnsupportedEnvironment
	}
	osID, err := vultrapi.ParseOSID(request.OSID)
	if err != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	label, err := provisionResourceLabel(request, osID)
	if err != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, err
	}
	tags := []string{"gsc-managed", "gsc-operation-" + request.OperationID}
	serverDigest := sha256.Sum256([]byte(request.Environment + "\x00" + request.ServerID))
	tags = append(tags, "gsc-target-"+hex.EncodeToString(serverDigest[:8]))
	create := vultrapi.CreateInstanceRequest{
		Region: request.Region, Plan: request.Plan, OSID: osID, Label: label,
		Hostname: request.Hostname, SSHKeyIDs: append([]string(nil), request.SSHKeyIDs...),
		StartupScriptID: request.StartupScriptID, FirewallGroupID: request.FirewallGroupID,
		Tags: tags,
	}
	if !validProvisionBootstrapReference(request) || vultrapi.ValidateCreateInstanceRequest(create) != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	reservation := store.CapacityReservation{
		OperationID: request.OperationID, ServerID: request.ServerID,
		EnvironmentID: request.Environment, CapacityGroup: groupID,
		Provider: providerID, ProviderAdapterID: "hosting.vultr", ResourceKey: label,
	}
	return create, reservation, nil
}

// Keep the JSON field order and omission rules stable for existing reservations.
func provisionResourceLabel(request CreateRequest, osID int) (string, error) {
	fingerprint, err := json.Marshal(struct {
		ServerID            string
		Environment         string
		Region              string
		Plan                string
		OSID                int
		Hostname            string
		SSHKeyIDs           []string
		StartupScriptID     string `json:",omitempty"`
		StartupScriptSHA256 string `json:",omitempty"`
		FirewallGroupID     string `json:",omitempty"`
	}{
		ServerID: request.ServerID, Environment: request.Environment,
		Region: request.Region, Plan: request.Plan, OSID: osID,
		Hostname: request.Hostname, SSHKeyIDs: request.SSHKeyIDs,
		StartupScriptID: request.StartupScriptID, StartupScriptSHA256: request.StartupScriptSHA256,
		FirewallGroupID: request.FirewallGroupID,
	})
	if err != nil {
		return "", ErrInvalidRequest
	}
	digest := sha256.Sum256(fingerprint)
	return "gsc-managed-" + request.OperationID + "-" + hex.EncodeToString(digest[:]), nil
}

func validProvisionBootstrapReference(request CreateRequest) bool {
	if request.StartupScriptID == "" {
		return request.StartupScriptSHA256 == ""
	}
	return sha256Pattern.MatchString(request.StartupScriptSHA256)
}
