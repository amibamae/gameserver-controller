package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the exact error validateServerBackupProfile
// returns for each rule, the boundary values each rule still accepts, and
// which rule wins when several are broken, so splitting it into rule groups
// keeps the same order and messages.

const (
	backupRefactorProfileMessage = "trusted backup profile must bind a release, offline consistency, bounded data sets/checks, and the server storage profile"
	backupRefactorUnitMessage    = "trusted backup profile unit reference does not bind the configured execution adapter and environment"
	backupRefactorChecksMessage  = "trusted backup validation checks require bounded IDs and versions"
)

func backupRefactorProfileServer() store.LogicalServer {
	return store.LogicalServer{ServerID: "server-1", EnvironmentID: "staging", ExecutionAdapterID: "execution.fake", StorageProfile: "storage-profile"}
}

// backupRefactorValidProfile returns a profile that passes every rule.
func backupRefactorValidProfile() ServerBackupProfile {
	return ServerBackupProfile{
		ProfileID: "profile-1", GameReleaseID: "paper-release", GameName: "Paper", GameVersion: "1.21",
		ConsistencyMode: "offline",
		DataSets: []ServerBackupDataSet{
			{DataSetID: "world", RelativePaths: []string{"world", "world_nether/DIM-1"}, Required: true, SupportedConsistencyModes: []string{"offline"}, MaximumBytes: 64},
			{DataSetID: "config", RelativePaths: []string{"server.properties"}, SupportedConsistencyModes: []string{"online", "offline"}, MaximumBytes: 1},
		},
		ValidationChecks: []ReadinessRequirement{{CheckID: "paper.backup.archive", CheckVersion: "1"}, {CheckID: "paper.backup.level", CheckVersion: "2"}},
		StorageAdapterID: "storage.fake", StorageProfileID: "storage-profile", StorageContainer: "backups",
		UnitRef: json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`),
	}
}

func backupRefactorDataSets(count int) []ServerBackupDataSet {
	dataSets := make([]ServerBackupDataSet, count)
	for i := range dataSets {
		dataSets[i] = ServerBackupDataSet{DataSetID: fmt.Sprintf("set-%d", i), RelativePaths: []string{"world"}, SupportedConsistencyModes: []string{"offline"}, MaximumBytes: 1}
	}
	return dataSets
}

func backupRefactorChecks(count int) []ReadinessRequirement {
	checks := make([]ReadinessRequirement, count)
	for i := range checks {
		checks[i] = ReadinessRequirement{CheckID: fmt.Sprintf("check-%d", i), CheckVersion: "1"}
	}
	return checks
}

func backupRefactorPaths(count int) []string {
	paths := make([]string, count)
	for i := range paths {
		paths[i] = fmt.Sprintf("path-%d", i)
	}
	return paths
}

func TestBackupRefactorValidateServerBackupProfile(t *testing.T) {
	incomplete := func(id string) string {
		return fmt.Sprintf("trusted backup profile contains an incomplete data-set declaration %q", id)
	}
	mode := func(id string) string {
		return fmt.Sprintf("trusted data set %q does not declare the selected consistency mode exactly once", id)
	}
	unsafe := func(id string) string { return fmt.Sprintf("trusted data set %q contains an unsafe relative path", id) }
	tests := []struct {
		name      string
		requested string
		server    func(*store.LogicalServer)
		change    func(*ServerBackupProfile)
		want      string
	}{
		{name: "valid profile"},

		// Profile binding rule.
		{name: "another requested profile", requested: "profile-2", want: backupRefactorProfileMessage},
		{name: "empty profile ID", requested: "-", change: func(p *ServerBackupProfile) { p.ProfileID = "" }, want: backupRefactorProfileMessage},
		{name: "empty release", change: func(p *ServerBackupProfile) { p.GameReleaseID = "" }, want: backupRefactorProfileMessage},
		{name: "release at the bound", change: func(p *ServerBackupProfile) { p.GameReleaseID = strings.Repeat("r", 500) }},
		{name: "release over the bound", change: func(p *ServerBackupProfile) { p.GameReleaseID = strings.Repeat("r", 501) }, want: backupRefactorProfileMessage},
		{name: "empty game name", change: func(p *ServerBackupProfile) { p.GameName = "" }, want: backupRefactorProfileMessage},
		{name: "game name at the bound", change: func(p *ServerBackupProfile) { p.GameName = strings.Repeat("n", 200) }},
		{name: "game name over the bound", change: func(p *ServerBackupProfile) { p.GameName = strings.Repeat("n", 201) }, want: backupRefactorProfileMessage},
		{name: "empty game version", change: func(p *ServerBackupProfile) { p.GameVersion = "" }, want: backupRefactorProfileMessage},
		{name: "game version at the bound", change: func(p *ServerBackupProfile) { p.GameVersion = strings.Repeat("v", 200) }},
		{name: "game version over the bound", change: func(p *ServerBackupProfile) { p.GameVersion = strings.Repeat("v", 201) }, want: backupRefactorProfileMessage},
		{name: "world format at the bound", change: func(p *ServerBackupProfile) { p.WorldFormat = strings.Repeat("w", 200) }},
		{name: "world format over the bound", change: func(p *ServerBackupProfile) { p.WorldFormat = strings.Repeat("w", 201) }, want: backupRefactorProfileMessage},
		{name: "online consistency", change: func(p *ServerBackupProfile) { p.ConsistencyMode = "online" }, want: backupRefactorProfileMessage},
		{name: "no data sets", change: func(p *ServerBackupProfile) { p.DataSets = nil }, want: backupRefactorProfileMessage},
		{name: "data sets at the bound", change: func(p *ServerBackupProfile) { p.DataSets = backupRefactorDataSets(128) }},
		{name: "data sets over the bound", change: func(p *ServerBackupProfile) { p.DataSets = backupRefactorDataSets(129) }, want: backupRefactorProfileMessage},
		{name: "no validation checks", change: func(p *ServerBackupProfile) { p.ValidationChecks = nil }, want: backupRefactorProfileMessage},
		{name: "validation checks at the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks = backupRefactorChecks(64) }},
		{name: "validation checks over the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks = backupRefactorChecks(65) }, want: backupRefactorProfileMessage},
		{name: "empty storage adapter", change: func(p *ServerBackupProfile) { p.StorageAdapterID = "" }, want: backupRefactorProfileMessage},
		{name: "another storage profile", server: func(s *store.LogicalServer) { s.StorageProfile = "other" }, want: backupRefactorProfileMessage},
		{name: "empty storage container", change: func(p *ServerBackupProfile) { p.StorageContainer = "" }, want: backupRefactorProfileMessage},
		{name: "storage container at the bound", change: func(p *ServerBackupProfile) { p.StorageContainer = strings.Repeat("c", 500) }},
		{name: "storage container over the bound", change: func(p *ServerBackupProfile) { p.StorageContainer = strings.Repeat("c", 501) }, want: backupRefactorProfileMessage},

		// Unit reference rule.
		{name: "missing unit", change: func(p *ServerBackupProfile) { p.UnitRef = nil }, want: backupRefactorUnitMessage},
		{name: "undecodable unit", change: func(p *ServerBackupProfile) { p.UnitRef = json.RawMessage(`{"adapter_id":`) }, want: backupRefactorUnitMessage},
		{name: "null unit", change: func(p *ServerBackupProfile) { p.UnitRef = json.RawMessage(`null`) }, want: backupRefactorUnitMessage},
		{name: "unit of another adapter", change: func(p *ServerBackupProfile) {
			p.UnitRef = json.RawMessage(`{"adapter_id":"execution.other","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`)
		}, want: backupRefactorUnitMessage},
		{name: "unit of another environment", change: func(p *ServerBackupProfile) {
			p.UnitRef = json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"prod","resource_kind":"systemd-unit","external_id":"unit-1"}`)
		}, want: backupRefactorUnitMessage},
		{name: "unit of another kind", change: func(p *ServerBackupProfile) {
			p.UnitRef = json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"container","external_id":"unit-1"}`)
		}, want: backupRefactorUnitMessage},
		{name: "unit without an external ID", change: func(p *ServerBackupProfile) {
			p.UnitRef = json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit"}`)
		}, want: backupRefactorUnitMessage},

		// Data-set declaration rules.
		{name: "data set without an ID", change: func(p *ServerBackupProfile) { p.DataSets[1].DataSetID = "" }, want: incomplete("")},
		{name: "data set ID at the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].DataSetID = strings.Repeat("d", 200) }},
		{name: "data set ID over the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].DataSetID = strings.Repeat("d", 201) }, want: incomplete(strings.Repeat("d", 201))},
		{name: "data set without a size", change: func(p *ServerBackupProfile) { p.DataSets[1].MaximumBytes = 0 }, want: incomplete("config")},
		{name: "data set size at the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].MaximumBytes = 9007199254740991 }},
		{name: "data set size over the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].MaximumBytes = 9007199254740992 }, want: incomplete("config")},
		{name: "data set without paths", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = nil }, want: incomplete("config")},
		{name: "data set paths at the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = backupRefactorPaths(128) }},
		{name: "data set paths over the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = backupRefactorPaths(129) }, want: incomplete("config")},
		{name: "data set without consistency modes", change: func(p *ServerBackupProfile) { p.DataSets[1].SupportedConsistencyModes = nil }, want: incomplete("config")},
		{name: "repeated data set", change: func(p *ServerBackupProfile) { p.DataSets[1].DataSetID = "world" }, want: `trusted backup profile repeats data set "world"`},
		{name: "data set without the offline mode", change: func(p *ServerBackupProfile) { p.DataSets[1].SupportedConsistencyModes = []string{"online"} }, want: mode("config")},
		{name: "data set repeating a mode", change: func(p *ServerBackupProfile) {
			p.DataSets[1].SupportedConsistencyModes = []string{"offline", "online", "online"}
		}, want: mode("config")},
		{name: "empty path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{""} }, want: unsafe("config")},
		{name: "path at the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{strings.Repeat("p", 512)} }},
		{name: "path over the bound", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{strings.Repeat("p", 513)} }, want: unsafe("config")},
		{name: "path with a backslash", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{`world\region`} }, want: unsafe("config")},
		{name: "absolute path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{"/world"} }, want: unsafe("config")},
		{name: "unclean path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{"world/"} }, want: unsafe("config")},
		{name: "dot path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{"."} }, want: unsafe("config")},
		{name: "parent path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{"../world"} }, want: unsafe("config")},
		{name: "parent segment", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{".."} }, want: unsafe("config")},
		{name: "repeated path", change: func(p *ServerBackupProfile) { p.DataSets[1].RelativePaths = []string{"world", "world"} }, want: `trusted data set "config" repeats a path`},

		// Validation check rules.
		{name: "check without an ID", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckID = "" }, want: backupRefactorChecksMessage},
		{name: "check ID at the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckID = strings.Repeat("c", 160) }},
		{name: "check ID over the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckID = strings.Repeat("c", 161) }, want: backupRefactorChecksMessage},
		{name: "check without a version", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckVersion = "" }, want: backupRefactorChecksMessage},
		{name: "check version at the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckVersion = strings.Repeat("1", 40) }},
		{name: "check version over the bound", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckVersion = strings.Repeat("1", 41) }, want: backupRefactorChecksMessage},
		{name: "repeated check", change: func(p *ServerBackupProfile) { p.ValidationChecks[1].CheckID = "paper.backup.archive" }, want: `trusted backup profile repeats validation check "paper.backup.archive"`},

		// Rule order when several rules are broken.
		{name: "profile binding before the unit", change: func(p *ServerBackupProfile) { p.GameName = ""; p.UnitRef = nil }, want: backupRefactorProfileMessage},
		{name: "unit before the data sets", change: func(p *ServerBackupProfile) { p.UnitRef = nil; p.DataSets[0].MaximumBytes = 0 }, want: backupRefactorUnitMessage},
		{name: "data sets before the checks", change: func(p *ServerBackupProfile) {
			p.DataSets[1].RelativePaths = []string{"/x"}
			p.ValidationChecks[0].CheckID = ""
		}, want: unsafe("config")},
		{name: "earlier data set first", change: func(p *ServerBackupProfile) {
			p.DataSets[0].RelativePaths = []string{"/x"}
			p.DataSets[1].MaximumBytes = 0
		}, want: unsafe("world")},
		{name: "incomplete before repeated", change: func(p *ServerBackupProfile) {
			p.DataSets[1].DataSetID = "world"
			p.DataSets[1].MaximumBytes = 0
		}, want: incomplete("world")},
		{name: "repeated data set before its mode", change: func(p *ServerBackupProfile) {
			p.DataSets[1].DataSetID = "world"
			p.DataSets[1].SupportedConsistencyModes = []string{"online"}
		}, want: `trusted backup profile repeats data set "world"`},
		{name: "mode before the paths", change: func(p *ServerBackupProfile) {
			p.DataSets[1].SupportedConsistencyModes = []string{"online"}
			p.DataSets[1].RelativePaths = []string{"/x"}
		}, want: mode("config")},
		{name: "unsafe path before a later repeat", change: func(p *ServerBackupProfile) {
			p.DataSets[1].RelativePaths = []string{"a", "a", "/x"}
		}, want: `trusted data set "config" repeats a path`},
		{name: "bounded check before a repeat", change: func(p *ServerBackupProfile) {
			p.ValidationChecks = []ReadinessRequirement{{CheckID: "a", CheckVersion: "1"}, {CheckID: "a", CheckVersion: ""}}
		}, want: backupRefactorChecksMessage},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := backupRefactorProfileServer()
			if test.server != nil {
				test.server(&server)
			}
			profile := backupRefactorValidProfile()
			if test.change != nil {
				test.change(&profile)
			}
			requested := profile.ProfileID
			if test.requested == "-" {
				requested = ""
			} else if test.requested != "" {
				requested = test.requested
			}
			err := validateServerBackupProfile(server, requested, profile)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validateServerBackupProfile() = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("validateServerBackupProfile() = %v, want %q", err, test.want)
			}
		})
	}
}
