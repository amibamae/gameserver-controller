package controllerruntime

import (
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/wasabistorage"
)

// Wasabi S3-compatible storage adapter (`implementation: wasabi`).

const (
	wasabiAccessKeyIDSecret     = "access_key_id"
	wasabiSecretAccessKeySecret = "secret_access_key"
)

// WasabiSettings is the `wasabi:` block. Keys come from secret_refs.
type WasabiSettings struct {
	StorageProfileID  string `yaml:"storage_profile_id"`
	EnvironmentID     string `yaml:"environment_id"`
	Container         string `yaml:"container"`
	KeyPrefix         string `yaml:"key_prefix"`
	Region            string `yaml:"region"`
	EndpointURL       string `yaml:"endpoint_url"`
	MaxObjectBytes    int64  `yaml:"max_object_bytes"`
	RequireVersionIDs bool   `yaml:"require_version_ids"`
}

func (w *WasabiSettings) BoundEnvironment() string { return w.EnvironmentID }

func (w *WasabiSettings) Validate(check config.SettingsCheck) {
	check.Require(strings.TrimSpace(w.StorageProfileID) != "" && len(w.StorageProfileID) <= 200, "storage_profile_id is required")
	check.Require(check.KnownEnvironment(w.EnvironmentID), "environment_id must name a configured environment")
	check.Require(w.Container != "" && len(w.Container) <= 500, "container is required")
	check.Require(strings.HasSuffix(w.KeyPrefix, "/") && len(w.KeyPrefix) <= 2048, "key_prefix must end with /")
	check.Require(w.Region != "", "region is required")
	check.Require(strings.HasPrefix(w.EndpointURL, "https://"), "endpoint_url must be an HTTPS URL")
	check.Require(w.MaxObjectBytes > 0, "max_object_bytes must be positive")
}

func wasabiConfig(profile config.AdapterProfile) wasabistorage.Config {
	w, ok := config.SettingsOf[*WasabiSettings](profile)
	if !ok {
		w = &WasabiSettings{}
	}
	return wasabistorage.Config{
		AdapterID: profile.ID, StorageProfileID: w.StorageProfileID, EnvironmentID: w.EnvironmentID,
		Container: w.Container, KeyPrefix: w.KeyPrefix, Region: w.Region, EndpointURL: w.EndpointURL,
		AccessKeyIDRef:     profile.SecretRefs[wasabiAccessKeyIDSecret].SecretRef,
		SecretAccessKeyRef: profile.SecretRefs[wasabiSecretAccessKeySecret].SecretRef,
		MaxObjectBytes:     w.MaxObjectBytes, RequireVersionIDs: w.RequireVersionIDs,
	}
}

func init() {
	registerAdapter(config.Implementation{
		Name: "wasabi", Category: "storage", Secrets: []string{wasabiAccessKeyIDSecret, wasabiSecretAccessKeySecret},
		NewSettings: func() config.ImplementationSettings { return &WasabiSettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error { return wasabistorage.ValidateConfig(wasabiConfig(b.profile)) },
		build: func(b adapterBuild) (builtAdapter, error) {
			credentials, err := b.credentials()
			if err != nil {
				return builtAdapter{}, err
			}
			adapter, err := wasabistorage.New(wasabiConfig(b.profile), b.protocolVersion, credentials)
			return builtAdapter{implementation: adapter}, err
		},
	})
}
