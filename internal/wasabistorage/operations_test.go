package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type codedError string

func (e codedError) Error() string     { return string(e) }
func (e codedError) ErrorCode() string { return string(e) }

func (m *memoryS3) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	bucket := aws.ToString(input.Bucket) + "/"
	var keys []string
	for full := range m.objects {
		if key, ok := strings.CutPrefix(full, bucket); ok && strings.HasPrefix(key, aws.ToString(input.Prefix)) && key > aws.ToString(input.ContinuationToken) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	limit := int(aws.ToInt32(input.MaxKeys))
	output := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(len(keys) > limit)}
	if len(keys) > limit {
		keys = keys[:limit]
		output.NextContinuationToken = aws.String(keys[len(keys)-1])
	}
	for _, key := range keys {
		output.Contents = append(output.Contents, types.Object{Key: aws.String(key), Size: aws.Int64(int64(len(m.objects[bucket+key]))), ETag: aws.String("\"fake-etag\"")})
	}
	return output, nil
}

func (m *memoryS3) CopyObject(_ context.Context, input *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	source, _, _ := strings.Cut(aws.ToString(input.CopySource), "?")
	source, err := url.PathUnescape(source)
	if err != nil {
		return nil, err
	}
	value, ok := m.objects[source]
	if !ok {
		return nil, codedError("NoSuchKey")
	}
	copied := append([]byte(nil), value...)
	if m.corruptCopy {
		copied = append(copied, '!')
	}
	m.objects[aws.ToString(input.Bucket)+"/"+aws.ToString(input.Key)] = copied
	m.copies++
	return &s3.CopyObjectOutput{VersionId: aws.String("version-1"), CopyObjectResult: &types.CopyObjectResult{ETag: aws.String("\"copy-etag\"")}}, nil
}

func (m *memoryS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if input.VersionId == nil {
		return nil, errors.New("test fake requires an exact version")
	}
	delete(m.objects, aws.ToString(input.Bucket)+"/"+aws.ToString(input.Key))
	m.deletes++
	return &s3.DeleteObjectOutput{VersionId: input.VersionId}, nil
}

func (m *memoryS3) GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	return &s3.GetBucketVersioningOutput{Status: m.versioning}, nil
}

func (m *memoryS3) GetObjectRetention(context.Context, *s3.GetObjectRetentionInput, ...func(*s3.Options)) (*s3.GetObjectRetentionOutput, error) {
	if m.noLock {
		return nil, codedError("ObjectLockConfigurationNotFoundError")
	}
	return &s3.GetObjectRetentionOutput{Retention: m.retention}, nil
}

func (m *memoryS3) GetObjectLegalHold(context.Context, *s3.GetObjectLegalHoldInput, ...func(*s3.Options)) (*s3.GetObjectLegalHoldOutput, error) {
	if m.noLock {
		return nil, codedError("ObjectLockConfigurationNotFoundError")
	}
	return &s3.GetObjectLegalHoldOutput{LegalHold: &types.ObjectLockLegalHold{Status: m.legalHold}}, nil
}

// registryFor registers the adapter so every input and output is checked against the protocol schemas.
func registryFor(t *testing.T, provider *memoryS3, config Config) (*adapter.Registry, *Adapter) {
	t.Helper()
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(impl); err != nil {
		t.Fatal(err)
	}
	return registry, impl
}

func newProvider(objects map[string]string) *memoryS3 {
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	for key, value := range objects {
		provider.objects["backups/"+key] = []byte(value)
	}
	return provider
}

func testContainer() containerReference {
	config := testConfig()
	return containerReference{AdapterID: config.AdapterID, StorageProfile: config.StorageProfileID, EnvironmentID: config.EnvironmentID, Container: config.Container}
}

func testRef(key, version string) objectReference {
	config := testConfig()
	return objectReference{objectTarget: objectTarget{AdapterID: config.AdapterID, StorageProfile: config.StorageProfileID,
		EnvironmentID: config.EnvironmentID, Container: config.Container, Key: key}, VersionID: version}
}

func decode(t *testing.T, response adapter.InvokeResponse, value any) {
	t.Helper()
	if err := json.Unmarshal(response.Result.Output, value); err != nil {
		t.Fatal(err)
	}
}

func checksumOf(value string) checksum {
	digest := sha256.Sum256([]byte(value))
	return checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}
}

func TestDescriptorAdvertisesAllImplementedCapabilities(t *testing.T) {
	_, impl := registryFor(t, newProvider(nil), testConfig())
	var names []string
	for _, capability := range impl.Descriptor().Capabilities {
		names = append(names, capability.Name)
	}
	want := "storage.object.put storage.object.get storage.object.head storage.object.list storage.object.copy storage.object.delete storage.object.versioning.inspect storage.object.retention.inspect"
	if strings.Join(names, " ") != want {
		t.Fatalf("capabilities = %v", names)
	}
}

func TestListPagesWithBoundContinuationTokens(t *testing.T) {
	provider := newProvider(map[string]string{"server/a": "1", "server/b": "22", "server/c": "333", "other/z": "x"})
	registry, _ := registryFor(t, provider, testConfig())
	deadline := time.Now().Add(time.Minute)
	var page struct {
		Items []struct {
			ObjectRef objectReference `json:"object_ref"`
			SizeBytes int64           `json:"size_bytes"`
		} `json:"items"`
		ListingState string  `json:"listing_state"`
		Next         *string `json:"next_continuation_token"`
	}
	decode(t, invoke(t, registry, "storage.object.list", listInput{ContainerRef: testContainer(), Prefix: "server/", MaximumItems: 2}, deadline, "", 0, nil, nil), &page)
	if page.ListingState != "truncated" || page.Next == nil || len(page.Items) != 2 || page.Items[0].ObjectRef.Key != "server/a" || page.Items[1].SizeBytes != 2 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	token := *page.Next
	page.Next = nil
	decode(t, invoke(t, registry, "storage.object.list", listInput{ContainerRef: testContainer(), Prefix: "server/", MaximumItems: 2, ContinuationToken: token}, deadline, "", 0, nil, nil), &page)
	if page.ListingState != "complete" || page.Next != nil || len(page.Items) != 1 || page.Items[0].ObjectRef.Key != "server/c" {
		t.Fatalf("unexpected second page: %+v", page)
	}

	// The token is bound to its prefix, cannot be altered, and only this adapter instance accepts it.
	_, otherInstance := registryFor(t, provider, testConfig())
	for name, input := range map[string]listInput{
		"other prefix":    {ContainerRef: testContainer(), Prefix: "server/b", MaximumItems: 2, ContinuationToken: token},
		"tampered token":  {ContainerRef: testContainer(), Prefix: "server/", MaximumItems: 2, ContinuationToken: token + "x"},
		"outside profile": {ContainerRef: testContainer(), Prefix: "other/", MaximumItems: 2},
	} {
		if _, err := registry.Invoke(context.Background(), testAdapterID, requestFor("storage.object.list", input, deadline, "", 0)); err == nil {
			t.Fatalf("%s: accepted an unauthorized listing", name)
		}
	}
	reused := listInput{ContainerRef: testContainer(), Prefix: "server/", MaximumItems: 2, ContinuationToken: token}
	if _, err := otherInstance.Invoke(context.Background(), requestFor("storage.object.list", reused, deadline, "", 0)); err == nil {
		t.Fatal("another adapter instance accepted the continuation token")
	}
}

func TestCopyVerifiesReadbackAndRefusesOverwrite(t *testing.T) {
	provider := newProvider(map[string]string{"server/world.tar": "world bytes", "server/taken.tar": "old"})
	registry, _ := registryFor(t, provider, testConfig())
	deadline := time.Now().Add(time.Minute)
	input := copyInput{SourceRef: testRef("server/world.tar", "version-1"), Destination: testRef("server/copy.tar", "").objectTarget,
		ExpectedSizeBytes: int64(len("world bytes")), ExpectedChecksum: checksumOf("world bytes")}
	var output struct {
		CopyState   string          `json:"copy_state"`
		BytesCopied int64           `json:"bytes_copied"`
		ObjectRef   objectReference `json:"object_ref"`
	}
	decode(t, invoke(t, registry, "storage.object.copy", input, deadline, "", 0, nil, nil), &output)
	if output.CopyState != "copied" || output.BytesCopied != int64(len("world bytes")) || output.ObjectRef.Key != "server/copy.tar" || string(provider.objects["backups/server/copy.tar"]) != "world bytes" {
		t.Fatalf("unexpected copy output: %+v", output)
	}

	overwrite := input
	overwrite.Destination = testRef("server/taken.tar", "").objectTarget
	if _, err := registry.Invoke(context.Background(), testAdapterID, requestFor("storage.object.copy", overwrite, deadline, "", 0)); err == nil || string(provider.objects["backups/server/taken.tar"]) != "old" {
		t.Fatal("copy overwrote an existing destination")
	}

	outside := input
	outside.Destination = objectTarget{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: "production/copy.tar"}
	if _, err := registry.Invoke(context.Background(), testAdapterID, requestFor("storage.object.copy", outside, deadline, "", 0)); err == nil {
		t.Fatal("copy wrote outside the profile's key prefix")
	}

	provider.corruptCopy = true
	corrupt := input
	corrupt.Destination = testRef("server/corrupt.tar", "").objectTarget
	_, err := registry.Invoke(context.Background(), testAdapterID, requestFor("storage.object.copy", corrupt, deadline, "", 0))
	var invocation *adapter.InvocationError
	if err == nil || !errors.As(err, &invocation) || !invocation.MayHaveSideEffects {
		t.Fatalf("corrupt copy must fail as a possible side effect, got %v", err)
	}
}

func TestDeleteRequiresExactVersionAndMatchingETag(t *testing.T) {
	provider := newProvider(map[string]string{"server/old.tar": "old backup"})
	registry, _ := registryFor(t, provider, testConfig())
	deadline := time.Now().Add(time.Minute)

	for name, input := range map[string]deleteInput{
		"no version":     {ObjectRef: testRef("server/old.tar", "")},
		"null version":   {ObjectRef: testRef("server/old.tar", "null")},
		"etag mismatch":  {ObjectRef: testRef("server/old.tar", "version-1"), IfMatchETag: "\"other\""},
		"outside prefix": {ObjectRef: objectReference{objectTarget: objectTarget{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: "production/old.tar"}, VersionID: "version-1"}},
	} {
		if _, err := registry.Invoke(context.Background(), testAdapterID, requestFor("storage.object.delete", input, deadline, "", 0)); err == nil {
			t.Fatalf("%s: delete was accepted", name)
		}
	}
	if provider.deletes != 0 {
		t.Fatalf("rejected deletes reached the provider %d times", provider.deletes)
	}

	var output struct {
		State string `json:"state"`
	}
	decode(t, invoke(t, registry, "storage.object.delete", deleteInput{ObjectRef: testRef("server/old.tar", "version-1"), IfMatchETag: "\"fake-etag\""}, deadline, "", 0, nil, nil), &output)
	if output.State != "absent" || provider.deletes != 1 {
		t.Fatalf("unexpected delete result: %+v, deletes=%d", output, provider.deletes)
	}
	decode(t, invoke(t, registry, "storage.object.delete", deleteInput{ObjectRef: testRef("server/old.tar", "version-1")}, deadline, "", 0, nil, nil), &output)
	if output.State != "absent" || provider.deletes != 1 {
		t.Fatal("deleting an absent version must report absent without another provider delete")
	}
}

func TestVersioningAndRetentionInspection(t *testing.T) {
	provider := newProvider(map[string]string{"server/world.tar": "w"})
	registry, _ := registryFor(t, provider, testConfig())
	deadline := time.Now().Add(time.Minute)

	var versioning struct {
		State string `json:"state"`
	}
	for status, want := range map[types.BucketVersioningStatus]string{"": "disabled", types.BucketVersioningStatusEnabled: "enabled", types.BucketVersioningStatusSuspended: "suspended"} {
		provider.versioning = status
		decode(t, invoke(t, registry, "storage.object.versioning.inspect", versioningInput{ContainerRef: testContainer()}, deadline, "", 0, nil, nil), &versioning)
		if versioning.State != want {
			t.Fatalf("versioning %q reported %q", status, versioning.State)
		}
	}

	var retention struct {
		State       string  `json:"state"`
		Mode        string  `json:"mode"`
		LegalHold   string  `json:"legal_hold"`
		RetainUntil *string `json:"retain_until"`
	}
	input := retentionInput{ObjectRef: testRef("server/world.tar", "version-1")}
	provider.noLock = true
	decode(t, invoke(t, registry, "storage.object.retention.inspect", input, deadline, "", 0, nil, nil), &retention)
	if retention.State != "observed" || retention.Mode != "none" || retention.LegalHold != "off" || retention.RetainUntil != nil {
		t.Fatalf("bucket without object lock: %+v", retention)
	}

	until := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	provider.noLock = false
	provider.retention = &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: &until}
	provider.legalHold = types.ObjectLockLegalHoldStatusOn
	decode(t, invoke(t, registry, "storage.object.retention.inspect", input, deadline, "", 0, nil, nil), &retention)
	if retention.State != "observed" || retention.Mode != "governance" || retention.LegalHold != "on" || retention.RetainUntil == nil || *retention.RetainUntil != "2027-01-02T03:04:05Z" {
		t.Fatalf("governance retention: %+v", retention)
	}
}
