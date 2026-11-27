package wasabistorage

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Characterization tests for storage.object.copy: request validation, the
// source and destination inspections, the CopyObject request, the version
// returned for the copy, readback verification and the result.

const (
	wasabiR5CopyInvalid  = "invalid object copy request"
	wasabiR5CopyVersion  = "an exact source object version is required by this storage profile"
	wasabiR5CopySize     = "Wasabi copy source size does not match the expected size"
	wasabiR5CopyMismatch = "Wasabi copy does not match the expected size and Checksum"
	wasabiR5CopyCalls    = "HeadObject HeadObject CopyObject GetObject"
)

func wasabiR5Copy(mutate func(*CopyInput)) CopyInput {
	value := CopyInput{SourceRef: wasabiR5Ref(nil), Destination: testRef("server/copy.tar", "").objectTarget,
		ExpectedSizeBytes: 5, ExpectedChecksum: checksumOf("hello")}
	storageR3Apply(mutate, &value)
	return value
}

func wasabiR5CopySource(version string) func(*CopyInput) {
	return func(in *CopyInput) { in.SourceRef.VersionID = version }
}

// wasabiR5CopyScript scripts a successful copy, then applies mutate.
func wasabiR5CopyScript(mutate func(*wasabiR5Client)) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) {
		c.heads = []wasabiR5Head{
			{out: &s3.HeadObjectOutput{ContentLength: aws.Int64(5), VersionId: aws.String("version-1")}},
			{err: wasabiR5Coded("NotFound")},
		}
		c.copyOut = &s3.CopyObjectOutput{VersionId: aws.String("version-2"), CopyObjectResult: &types.CopyObjectResult{ETag: aws.String("\"copy-etag\"")}}
		c.getBody = strings.NewReader("hello")
		storageR3Apply(mutate, c)
	}
}

func wasabiR5CopyHead(index int, head wasabiR5Head) func(*wasabiR5Client) {
	return wasabiR5CopyScript(func(c *wasabiR5Client) { c.heads[index] = head })
}

func wasabiR5CopyBody(body io.Reader) func(*wasabiR5Client) {
	return wasabiR5CopyScript(func(c *wasabiR5Client) { c.getBody = body })
}

func wasabiR5CopyResult(result *s3.CopyObjectOutput) func(*wasabiR5Client) {
	return wasabiR5CopyScript(func(c *wasabiR5Client) { c.copyOut = result })
}

// wasabiR5Copied is the expected output of a verified copy.
func wasabiR5Copied(t *testing.T, version string, etag any) string {
	t.Helper()
	value := map[string]any{"destination": testRef("server/copy.tar", "").objectTarget, "copy_state": "copied",
		"object_ref": testRef("server/copy.tar", version), "bytes_copied": 5}
	if etag != nil {
		value["provider_etag"] = etag
	}
	return wasabiR5Output(t, value)
}

func TestWasabiR5CopyRejectsBeforeProvider(t *testing.T) {
	sixGiB := func(config *Config) { config.MaxObjectBytes = 6 << 30 }
	oversize := func(in *CopyInput) { in.ExpectedSizeBytes = maxServerSideCopySize + 1 }
	sameKey := func(in *CopyInput) { in.Destination.Key = in.SourceRef.Key }
	wasabiR5Run(t, "storage.object.copy", []wasabiR5Case{
		{name: "malformed input", input: wasabiR5Copy(nil), mutate: wasabiR5BadJSON, want: wasabiR5CopyInvalid},
		{name: "request environment", input: wasabiR5Copy(nil), mutate: wasabiR5WrongEnvironment, want: wasabiR5CopyInvalid},
		{name: "source outside prefix", input: wasabiR5Copy(func(in *CopyInput) { wasabiR5OutsideRef(&in.SourceRef) }), want: wasabiR5CopyInvalid},
		{name: "source bucket", input: wasabiR5Copy(func(in *CopyInput) { wasabiR5OtherBucket(&in.SourceRef) }), want: wasabiR5CopyInvalid},
		{name: "destination outside prefix", input: wasabiR5Copy(func(in *CopyInput) { in.Destination.Key = "production/copy.tar" }), want: wasabiR5CopyInvalid},
		{name: "destination profile", input: wasabiR5Copy(func(in *CopyInput) { in.Destination.StorageProfile = "other" }), want: wasabiR5CopyInvalid},
		{name: "negative size", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedSizeBytes = -1 }), want: wasabiR5CopyInvalid},
		{name: "size above profile", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedSizeBytes = 1<<30 + 1 }), want: wasabiR5CopyInvalid},
		{name: "Checksum algorithm", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedChecksum.Algorithm = "md5" }), want: wasabiR5CopyInvalid},
		{name: "Checksum length", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedChecksum.Digest = "abcd" }), want: wasabiR5CopyInvalid},
		{name: "Checksum hex", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedChecksum.Digest = strings.Repeat("z", 64) }), want: wasabiR5CopyInvalid},
		{name: "invalid before version", input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedSizeBytes = -1; in.SourceRef.VersionID = "null" }), want: wasabiR5CopyInvalid},
		{name: "required version missing", config: wasabiR5RequireVersions, input: wasabiR5Copy(wasabiR5CopySource("")), want: wasabiR5CopyVersion},
		{name: "required version null", config: wasabiR5RequireVersions, input: wasabiR5Copy(wasabiR5CopySource("null")), want: wasabiR5CopyVersion},
		{name: "null version", input: wasabiR5Copy(wasabiR5CopySource("null")), want: wasabiR5CopyVersion},
		{name: "version before same key", input: wasabiR5Copy(func(in *CopyInput) { sameKey(in); in.SourceRef.VersionID = "null" }), want: wasabiR5CopyVersion},
		{name: "same key", input: wasabiR5Copy(sameKey), want: "copy destination must differ from its source"},
		{name: "same key before size", config: sixGiB, input: wasabiR5Copy(func(in *CopyInput) { sameKey(in); oversize(in) }), want: "copy destination must differ from its source"},
		{name: "above copy limit", config: sixGiB, input: wasabiR5Copy(oversize), mutate: wasabiR5NoSecrets, want: "object is larger than the single-request server-side copy limit"},
		{name: "credentials", input: wasabiR5Copy(nil), mutate: wasabiR5NoSecrets, want: wasabiR5Credentials},
	})
}

func TestWasabiR5CopyInspections(t *testing.T) {
	sourceHead := func(version string) func(*testing.T, *wasabiR5Client) {
		return func(t *testing.T, c *wasabiR5Client) {
			wasabiR5Sent(t, "source head", c.headIn[0].Bucket, c.headIn[0].Key, c.headIn[0].VersionId, version)
		}
	}
	destinationHead := func(t *testing.T, c *wasabiR5Client) {
		storageR3Equal(t, "destination bucket", aws.ToString(c.headIn[1].Bucket), "backups")
		storageR3Equal(t, "destination key", aws.ToString(c.headIn[1].Key), "server/copy.tar")
		storageR3Equal(t, "destination version set", c.headIn[1].VersionId != nil, false)
	}
	sized := func(size *int64, version *string) wasabiR5Head {
		return wasabiR5Head{out: &s3.HeadObjectOutput{ContentLength: size, VersionId: version}}
	}
	wasabiR5Run(t, "storage.object.copy", []wasabiR5Case{
		{name: "source inspection fails", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(0, wasabiR5Head{err: wasabiR5Coded("NotFound")}),
			calls: "HeadObject", want: "Wasabi copy source could not be inspected", check: sourceHead("version-1")},
		{name: "source version missing", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(0, sized(aws.Int64(5), nil)),
			calls: "HeadObject", want: wasabiR5Unconfirmed},
		{name: "source version differs", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(0, sized(aws.Int64(5), aws.String("version-9"))),
			calls: "HeadObject", want: wasabiR5Unconfirmed},
		{name: "source size missing", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(0, sized(nil, aws.String("version-1"))),
			calls: "HeadObject", want: wasabiR5CopySize},
		{name: "source size differs", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(0, sized(aws.Int64(4), aws.String("version-1"))),
			calls: "HeadObject", want: wasabiR5CopySize},
		{name: "size at copy limit", config: func(c *Config) { c.MaxObjectBytes = 6 << 30 },
			input: wasabiR5Copy(func(in *CopyInput) { in.ExpectedSizeBytes = maxServerSideCopySize }), script: wasabiR5CopyScript(nil),
			calls: "HeadObject", want: wasabiR5CopySize},
		{name: "unversioned source", input: wasabiR5Copy(wasabiR5CopySource("")), script: wasabiR5CopyHead(0, sized(aws.Int64(5), aws.String("other"))),
			calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: sourceHead("")},
		{name: "destination exists", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(1, sized(aws.Int64(1), nil)),
			calls: "HeadObject HeadObject", want: "copy destination already exists; refusing to overwrite it", check: destinationHead},
		{name: "destination inspection fails", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(1, wasabiR5Head{err: wasabiR5Coded("AccessDenied")}),
			calls: "HeadObject HeadObject", want: "Wasabi copy destination could not be inspected"},
		{name: "destination uncoded failure", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(1, wasabiR5Head{err: errors.New("timeout")}),
			calls: "HeadObject HeadObject", want: "Wasabi copy destination could not be inspected"},
		{name: "destination no such key", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(1, wasabiR5Head{err: wasabiR5Coded("NoSuchKey")}),
			calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", "\"copy-etag\"")},
		{name: "destination no such version", input: wasabiR5Copy(nil), script: wasabiR5CopyHead(1, wasabiR5Head{err: wasabiR5Coded("NoSuchVersion")}),
			calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: destinationHead},
	})
}

func TestWasabiR5CopyRequest(t *testing.T) {
	copied := func(source string) func(*testing.T, *wasabiR5Client) {
		return func(t *testing.T, c *wasabiR5Client) {
			storageR3Equal(t, "copy bucket", aws.ToString(c.copyIn.Bucket), "backups")
			storageR3Equal(t, "copy key", aws.ToString(c.copyIn.Key), "server/copy.tar")
			storageR3Equal(t, "copy source", aws.ToString(c.copyIn.CopySource), source)
			storageR3Equal(t, "copy Checksum", c.copyIn.ChecksumAlgorithm, types.ChecksumAlgorithmSha256)
		}
	}
	escaped := func(in *CopyInput) { in.SourceRef.Key = "server/a b/c+d%.tar"; in.SourceRef.VersionID = "v 1&x/y" }
	escapedHead := wasabiR5CopyHead(0, wasabiR5Head{out: &s3.HeadObjectOutput{ContentLength: aws.Int64(5), VersionId: aws.String("v 1&x/y")}})
	wasabiR5Run(t, "storage.object.copy", []wasabiR5Case{
		{name: "versioned source", input: wasabiR5Copy(nil), script: wasabiR5CopyScript(nil), calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: copied("backups/server/world.tar?versionId=version-1")},
		{name: "unversioned source", input: wasabiR5Copy(wasabiR5CopySource("")), script: wasabiR5CopyScript(nil), calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: copied("backups/server/world.tar")},
		{name: "escaped source", input: wasabiR5Copy(escaped), script: escapedHead, calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: copied("backups/server/a%20b/c+d%25.tar?versionId=v+1%26x%2Fy")},
		{name: "provider failure", input: wasabiR5Copy(nil), script: wasabiR5CopyScript(func(c *wasabiR5Client) { c.copyErr = wasabiR5Coded("InternalError") }),
			calls: "HeadObject HeadObject CopyObject", want: "Wasabi server-side copy failed", sideEffect: true,
			check: copied("backups/server/world.tar?versionId=version-1")},
	})
}

func TestWasabiR5CopyVersionAndReadback(t *testing.T) {
	readback := func(version string) func(*testing.T, *wasabiR5Client) {
		return func(t *testing.T, c *wasabiR5Client) {
			storageR3Equal(t, "readback bucket", aws.ToString(c.getIn.Bucket), "backups")
			storageR3Equal(t, "readback key", aws.ToString(c.getIn.Key), "server/copy.tar")
			storageR3Equal(t, "readback version set", c.getIn.VersionId != nil, version != "")
			storageR3Equal(t, "readback version", aws.ToString(c.getIn.VersionId), version)
			storageR3Equal(t, "readback closed", c.body.closed, 1)
		}
	}
	versioned := func(version *string) func(*wasabiR5Client) {
		return wasabiR5CopyResult(&s3.CopyObjectOutput{VersionId: version})
	}
	const noVersion = "Wasabi did not return the required immutable version for the copy"
	upper := func(in *CopyInput) { in.ExpectedChecksum.Digest = strings.ToUpper(in.ExpectedChecksum.Digest) }
	wasabiR5Run(t, "storage.object.copy", []wasabiR5Case{
		{name: "required version missing", config: wasabiR5RequireVersions, input: wasabiR5Copy(nil), script: versioned(nil),
			calls: "HeadObject HeadObject CopyObject", want: noVersion, sideEffect: true},
		{name: "required version null", config: wasabiR5RequireVersions, input: wasabiR5Copy(nil), script: versioned(aws.String("null")),
			calls: "HeadObject HeadObject CopyObject", want: noVersion, sideEffect: true},
		{name: "required version present", config: wasabiR5RequireVersions, input: wasabiR5Copy(nil), script: wasabiR5CopyScript(nil),
			calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: readback("version-2")},
		{name: "no version", input: wasabiR5Copy(nil), script: versioned(nil), calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "", nil), check: readback("")},
		{name: "null version", input: wasabiR5Copy(nil), script: versioned(aws.String("null")), calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "", nil), check: readback("")},
		{name: "readback fails", input: wasabiR5Copy(nil), script: wasabiR5CopyScript(func(c *wasabiR5Client) { c.getErr = wasabiR5Coded("NoSuchKey") }),
			calls: wasabiR5CopyCalls, want: "Wasabi copy could not be read back for verification", sideEffect: true},
		{name: "readback error", input: wasabiR5Copy(nil), script: wasabiR5CopyBody(io.MultiReader(strings.NewReader("hello"), iotest.ErrReader(errors.New("reset")))),
			calls: wasabiR5CopyCalls, want: wasabiR5CopyMismatch, sideEffect: true, check: readback("version-2")},
		{name: "readback short", input: wasabiR5Copy(nil), script: wasabiR5CopyBody(strings.NewReader("hell")),
			calls: wasabiR5CopyCalls, want: wasabiR5CopyMismatch, sideEffect: true, check: readback("version-2")},
		{name: "readback long", input: wasabiR5Copy(nil), script: wasabiR5CopyBody(strings.NewReader("hello!")),
			calls: wasabiR5CopyCalls, want: wasabiR5CopyMismatch, sideEffect: true},
		{name: "readback Checksum", input: wasabiR5Copy(nil), script: wasabiR5CopyBody(strings.NewReader("jello")),
			calls: wasabiR5CopyCalls, want: wasabiR5CopyMismatch, sideEffect: true},
		{name: "upper-case digest", input: wasabiR5Copy(upper), script: wasabiR5CopyScript(nil), calls: wasabiR5CopyCalls,
			output: wasabiR5Copied(t, "version-2", "\"copy-etag\""), check: readback("version-2")},
	})
}

func TestWasabiR5CopyETag(t *testing.T) {
	result := func(etag *string) *s3.CopyObjectOutput {
		return &s3.CopyObjectOutput{VersionId: aws.String("version-2"), CopyObjectResult: &types.CopyObjectResult{ETag: etag}}
	}
	wasabiR5Run(t, "storage.object.copy", []wasabiR5Case{
		{name: "no copy result", input: wasabiR5Copy(nil), script: wasabiR5CopyResult(&s3.CopyObjectOutput{VersionId: aws.String("version-2")}),
			calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", nil)},
		{name: "no etag", input: wasabiR5Copy(nil), script: wasabiR5CopyResult(result(nil)), calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", nil)},
		{name: "empty etag", input: wasabiR5Copy(nil), script: wasabiR5CopyResult(result(aws.String(""))), calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", nil)},
		{name: "etag", input: wasabiR5Copy(nil), script: wasabiR5CopyResult(result(aws.String("\"e\""))), calls: wasabiR5CopyCalls, output: wasabiR5Copied(t, "version-2", "\"e\"")},
	})
}
