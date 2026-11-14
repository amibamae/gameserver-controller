package wasabistorage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// These characterization tests pin profile validation, construction, the
// descriptor, and the small transfer helpers with their exact error text.

type storageR3ConfigCase struct {
	name   string
	mutate func(*Config)
	want   string
}

func (c storageR3ConfigCase) run(t *testing.T) {
	config := storageR3Config(c.mutate)
	text := storageR3Text(validateConfig(config))
	storageR3Equal(t, "validateConfig", text, c.want)
	text = storageR3Text(ValidateConfig(config))
	storageR3Equal(t, "ValidateConfig", text, c.want)
	impl, err := New(config, testProtocolVersion, testCredentials{})
	text = storageR3Text(err)
	storageR3Equal(t, "New", text, c.want)
	storageR3Equal(t, "New returned an adapter", impl != nil, c.want == "")
}

func storageR3Endpoint(endpoint string) func(*Config) {
	return func(c *Config) { c.EndpointURL = endpoint }
}

func TestStorageR3ConfigErrorText(t *testing.T) {
	const identity = "storage profile, environment, container, key prefix, and Wasabi region are required"
	const limit = "maximum object size must be between 1 byte and the bounded multipart limit"
	const refs = "separate systemd credential references for both Wasabi keys are required"
	const shape = "Wasabi endpoint must be an HTTPS URL without user information, query, or fragment"
	const host = "Wasabi endpoint host must be within wasabisys.com"
	const port = "Wasabi endpoint must use HTTPS port 443"
	tests := []storageR3ConfigCase{
		{"valid", nil, ""},
		{"adapter id pattern", func(c *Config) { c.AdapterID = "Storage.wasabi" }, identity},
		{"adapter id prefix", func(c *Config) { c.AdapterID = "backup.wasabi" }, identity},
		{"storage profile", func(c *Config) { c.StorageProfileID = "" }, identity},
		{"environment", func(c *Config) { c.EnvironmentID = "" }, identity},
		{"container empty", func(c *Config) { c.Container = "" }, identity},
		{"container long", func(c *Config) { c.Container = strings.Repeat("b", 501) }, identity},
		{"container at limit", func(c *Config) { c.Container = strings.Repeat("b", 500) }, ""},
		{"key prefix slash", func(c *Config) { c.KeyPrefix = "server" }, identity},
		{"key prefix long", func(c *Config) { c.KeyPrefix = strings.Repeat("k", 2048) + "/" }, identity},
		{"key prefix at limit", func(c *Config) { c.KeyPrefix = strings.Repeat("k", 2047) + "/" }, ""},
		{"region", func(c *Config) { c.Region = "US-West-1" }, identity},
		{"region trailing dash", func(c *Config) { c.Region = "us-west-" }, identity},
		{"object size zero", func(c *Config) { c.MaxObjectBytes = 0 }, limit},
		{"object size above S3", func(c *Config) { c.MaxObjectBytes = maxS3ObjectSize + 1 }, limit},
		{"object size above parts", func(c *Config) { c.MaxObjectBytes = maxPartSize*maxMultipartParts + 1 }, limit},
		{"object size at parts", func(c *Config) { c.MaxObjectBytes = maxPartSize * maxMultipartParts }, ""},
		{"object size one", func(c *Config) { c.MaxObjectBytes = 1 }, ""},
		{"access ref scheme", func(c *Config) { c.AccessKeyIDRef = "env://access" }, refs},
		{"secret ref scheme", func(c *Config) { c.SecretAccessKeyRef = "env://secret" }, refs},
		{"shared refs", func(c *Config) { c.SecretAccessKeyRef = c.AccessKeyIDRef }, refs},
		{"endpoint parse", storageR3Endpoint("https://[::1"), shape},
		{"endpoint scheme", storageR3Endpoint("http://s3.wasabisys.com"), shape},
		{"endpoint user", storageR3Endpoint("https://user@s3.wasabisys.com"), shape},
		{"endpoint query", storageR3Endpoint("https://s3.wasabisys.com?a=b"), shape},
		{"endpoint fragment", storageR3Endpoint("https://s3.wasabisys.com#frag"), shape},
		{"endpoint host empty", storageR3Endpoint("https:///"), shape},
		{"endpoint path", storageR3Endpoint("https://s3.wasabisys.com/bucket"), shape},
		{"endpoint root path", storageR3Endpoint("https://s3.wasabisys.com/"), ""},
		{"endpoint ip", storageR3Endpoint("https://1.2.3.4"), host},
		{"endpoint ipv6", storageR3Endpoint("https://[::1]"), host},
		{"endpoint lookalike suffix", storageR3Endpoint("https://evilwasabisys.com"), host},
		{"endpoint lookalike parent", storageR3Endpoint("https://wasabisys.com.example.com"), host},
		{"endpoint apex upper dot", storageR3Endpoint("https://WASABISYS.COM."), ""},
		{"endpoint port", storageR3Endpoint("https://s3.wasabisys.com:8443"), port},
		{"endpoint port 443", storageR3Endpoint("https://s3.wasabisys.com:443"), ""},
		{"endpoint bad host before port", storageR3Endpoint("https://example.com:8443"), host},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestStorageR3NewAdapterRequirements(t *testing.T) {
	const want = "protocol version, credential resolver, and S3 client factory are required"
	factory := func(_, _, _, _ string) s3Client { return storageR3NewClient() }
	builds := map[string]func() (*Adapter, error){
		"protocol":   func() (*Adapter, error) { return newAdapter(testConfig(), "", testCredentials{}, factory) },
		"resolver":   func() (*Adapter, error) { return newAdapter(testConfig(), testProtocolVersion, nil, factory) },
		"factory":    func() (*Adapter, error) { return newAdapter(testConfig(), testProtocolVersion, testCredentials{}, nil) },
		"public new": func() (*Adapter, error) { return New(testConfig(), "", testCredentials{}) },
	}
	for name, build := range builds {
		impl, err := build()
		text := storageR3Text(err)
		storageR3Equal(t, name+" error", text, want)
		storageR3Equal(t, name+" adapter", impl, nil)
	}
	_, err := newAdapter(storageR3Config(func(c *Config) { c.Region = "" }), "", nil, nil)
	text := storageR3Text(err)
	storageR3Equal(t, "config checked before dependencies", text, "storage profile, environment, container, key prefix, and Wasabi region are required")
	impl, err := New(testConfig(), testProtocolVersion, testCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "client factory set", impl.newClient != nil, true)
	storageR3Equal(t, "token key drawn", impl.tokenKey != [32]byte{}, true)
	client := newS3Client("us-west-1", "https://s3.us-west-1.wasabisys.com", "access", "secret")
	storageR3Equal(t, "S3 client built", client != nil, true)
}

func TestStorageR3DescriptorShape(t *testing.T) {
	impl := storageR3Adapter(t, testConfig(), storageR3NewClient(), nil, nil)
	urn := func(schema string) string {
		return `"input_schema_uri":"urn:gameserver-controller:0.21.0-draft:storage:` + schema + `-input","output_schema_uri":"urn:gameserver-controller:0.21.0-draft:storage:` + schema + `-output"`
	}
	want := `{"adapter_id":"storage.wasabi-staging","category":"storage","display_name":"Wasabi S3-compatible object storage","implementation_version":"0.1.0","protocol_versions":["0.21.0-draft"],"contract_versions":["1"],"required_permissions":[],"capabilities":[` +
		`{"name":"storage.object.put","version":"1","mutating":true,` + urn("object-put") + `,"constraints":{"maximum_object_bytes":1073741824,"stream_transfer_direction":"controller_to_adapter"}},` +
		`{"name":"storage.object.get","version":"1","mutating":false,` + urn("object-get") + `,"constraints":{"maximum_object_bytes":1073741824,"stream_transfer_direction":"adapter_to_controller"}},` +
		`{"name":"storage.object.head","version":"1","mutating":false,` + urn("object-head") + `},` +
		`{"name":"storage.object.list","version":"1","mutating":false,` + urn("object-list") + `},` +
		`{"name":"storage.object.copy","version":"1","mutating":true,` + urn("object-copy") + `},` +
		`{"name":"storage.object.delete","version":"1","mutating":true,` + urn("object-delete") + `},` +
		`{"name":"storage.object.versioning.inspect","version":"1","mutating":false,` + urn("object-versioning-inspect") + `},` +
		`{"name":"storage.object.retention.inspect","version":"1","mutating":false,` + urn("object-retention-inspect") + `}],` +
		`"stream_transport_profiles":["local-unix-stream-v1"]}`
	storageR3Equal(t, "descriptor", storageR3JSON(t, impl.Descriptor()), want)
	for _, capability := range impl.Descriptor().Capabilities {
		storageR3Equal(t, capability.Name+" permissions", storageR3JSON(t, capability.RequiredPermissions), "[]")
	}
}

func TestStorageR3PartSize(t *testing.T) {
	for _, test := range []struct{ size, want int64 }{
		{0, minPartSize},
		{1, minPartSize},
		{minPartSize * maxMultipartParts, minPartSize},
		{minPartSize*maxMultipartParts + 1, minPartSize + 1},
		{maxPartSize * maxMultipartParts, maxPartSize},
	} {
		storageR3Equal(t, "partSize", partSize(test.size), test.want)
	}
}

func TestStorageR3ConfirmedVersion(t *testing.T) {
	unconfirmed := "Wasabi did not confirm the requested object version"
	null := "the mutable null object version is not accepted as an exact version"
	tests := []struct {
		requested string
		observed  *string
		want      string
		err       string
	}{
		{"", nil, "", ""},
		{"", aws.String(""), "", ""},
		{"", aws.String("null"), "", ""},
		{"", aws.String("v2"), "v2", ""},
		{"v1", aws.String("v1"), "v1", ""},
		{"v1", aws.String("v2"), "", unconfirmed},
		{"v1", nil, "", unconfirmed},
		{"null", aws.String("null"), "", null},
		{"null", nil, "", null},
	}
	for _, test := range tests {
		got, err := confirmedVersion(test.requested, test.observed)
		text := storageR3Text(err)
		storageR3Equal(t, "confirmedVersion("+test.requested+", "+aws.ToString(test.observed)+")", got+"|"+text, test.want+"|"+test.err)
	}
}

func TestStorageR3SingleTransferBoundary(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	request := adapter.InvokeRequest{Params: adapter.InvokeParams{RequestContext: adapter.RequestContext{Deadline: deadline,
		StreamTransfers: []capturestream.Reference{{TransferID: testTransferID(), Direction: "controller_to_adapter", MaximumBytes: 10, ExpiresAt: deadline}}}}}
	transfer, err := singleTransfer(request, "controller_to_adapter", 10)
	text := storageR3Text(err)
	storageR3Equal(t, "transfer at the deadline with exact capacity", text, "")
	storageR3Equal(t, "transfer ID", transfer.TransferID, testTransferID())
	_, err = singleTransfer(request, "controller_to_adapter", 11)
	text = storageR3Text(err)
	storageR3Equal(t, "over-capacity transfer", text, "storage stream handle does not match the bounded transfer")
}

func TestStorageR3StageVerifiedUploadKeepsVerifiedBytes(t *testing.T) {
	tempDir := storageR3PrivateTemp(t)
	file, err := stageVerifiedUpload(bytes.NewReader(storageR3Content), int64(len(storageR3Content)), strings.ToUpper(storageR3Checksum(storageR3Content).Digest))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		if removeErr := os.Remove(file.Name()); removeErr != nil {
			t.Error(removeErr)
		}
	})
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "staging directory", filepath.Dir(file.Name()), tempDir)
	storageR3Equal(t, "staging name prefix", strings.HasPrefix(filepath.Base(file.Name()), "gsc-wasabi-upload-"), true)
	storageR3Equal(t, "staging mode", info.Mode(), os.FileMode(0600))
	staged, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "staged bytes from offset zero", string(staged), string(storageR3Content))
}

func TestStorageR3StageVerifiedUploadRemovesRejectedBytes(t *testing.T) {
	streams := map[string][]byte{
		"short":   storageR3Content[:2],
		"long":    append(append([]byte(nil), storageR3Content...), '!'),
		"altered": bytes.Repeat([]byte("x"), len(storageR3Content)),
	}
	for name, stream := range streams {
		t.Run(name, func(t *testing.T) {
			tempDir := storageR3PrivateTemp(t)
			file, err := stageVerifiedUpload(bytes.NewReader(stream), int64(len(storageR3Content)), storageR3Checksum(storageR3Content).Digest)
			text := storageR3Text(err)
			storageR3Equal(t, "error", text, "upload data does not match the declared size and checksum")
			storageR3Equal(t, "file", file, nil)
			storageR3NoStaging(t, tempDir)
		})
	}
}

func TestStorageR3StagingUnavailable(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	file, err := stageVerifiedUpload(bytes.NewReader(storageR3Content), int64(len(storageR3Content)), storageR3Checksum(storageR3Content).Digest)
	text := storageR3Text(err)
	storageR3Equal(t, "stageVerifiedUpload error", text, "private upload staging is unavailable")
	storageR3Equal(t, "file", file, nil)
	impl := storageR3Adapter(t, testConfig(), storageR3NewClient(), nil, nil)
	request, source := storageR3PutRequest(storageR3PutInput(), storageR3Content)
	response, err := impl.Invoke(context.Background(), request)
	storageR3Failed(t, response, err, "upload stream did not match its declared size and checksum", false)
	storageR3Equal(t, "upload stream closes", source.closed, 1)
}
