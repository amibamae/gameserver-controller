package wasabistorage

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Characterization tests for storage.object.delete: request validation, the
// exact-version inspection before deletion, the ETag guard, the DeleteObject
// request and the state reported from the inspection afterwards.

const (
	wasabiR5DeleteInvalid = "invalid object delete request"
	wasabiR5DeleteVersion = "deletion requires an exact object version"
	wasabiR5DeleteCalls   = "HeadObject DeleteObject HeadObject"
)

func wasabiR5Delete(mutate func(*deleteInput)) deleteInput {
	value := deleteInput{ObjectRef: wasabiR5Ref(nil)}
	storageR3Apply(mutate, &value)
	return value
}

func wasabiR5DeleteETag(etag string) deleteInput {
	return wasabiR5Delete(func(in *deleteInput) { in.IfMatchETag = etag })
}

// wasabiR5DeleteScript scripts the inspection before and after deletion.
func wasabiR5DeleteScript(before, after wasabiR5Head) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) { c.heads = []wasabiR5Head{before, after} }
}

func wasabiR5Present(etag, version *string) wasabiR5Head {
	return wasabiR5Head{out: &s3.HeadObjectOutput{ETag: etag, VersionId: version}}
}

func wasabiR5Deleted(t *testing.T, state string) string {
	t.Helper()
	return wasabiR5Output(t, map[string]any{"object_ref": wasabiR5Ref(nil), "state": state})
}

func TestWasabiR5DeleteRejectsBeforeProvider(t *testing.T) {
	wasabiR5Run(t, "storage.object.delete", []wasabiR5Case{
		{name: "malformed input", input: wasabiR5Delete(nil), mutate: wasabiR5BadJSON, want: wasabiR5DeleteInvalid},
		{name: "request environment", input: wasabiR5Delete(nil), mutate: wasabiR5WrongEnvironment, want: wasabiR5DeleteInvalid},
		{name: "outside prefix", input: wasabiR5Delete(func(in *deleteInput) { wasabiR5OutsideRef(&in.ObjectRef) }), want: wasabiR5DeleteInvalid},
		{name: "other bucket", input: wasabiR5Delete(func(in *deleteInput) { wasabiR5OtherBucket(&in.ObjectRef) }), want: wasabiR5DeleteInvalid},
		{name: "invalid before version", input: wasabiR5Delete(func(in *deleteInput) { wasabiR5OutsideRef(&in.ObjectRef); in.ObjectRef.VersionID = "" }), want: wasabiR5DeleteInvalid},
		{name: "no version", input: wasabiR5Delete(func(in *deleteInput) { in.ObjectRef.VersionID = "" }), want: wasabiR5DeleteVersion},
		{name: "null version", input: wasabiR5Delete(func(in *deleteInput) { in.ObjectRef.VersionID = "null" }), want: wasabiR5DeleteVersion},
		{name: "version before credentials", input: wasabiR5Delete(func(in *deleteInput) { in.ObjectRef.VersionID = "" }), mutate: wasabiR5NoSecrets, want: wasabiR5DeleteVersion},
		{name: "credentials", input: wasabiR5Delete(nil), mutate: wasabiR5NoSecrets, want: wasabiR5Credentials},
	})
}

func TestWasabiR5DeleteFlow(t *testing.T) {
	present := wasabiR5Present(aws.String("\"etag\""), aws.String("version-1"))
	gone := wasabiR5Head{err: wasabiR5Coded("NotFound")}
	sent := func(t *testing.T, c *wasabiR5Client) {
		for _, head := range c.headIn {
			wasabiR5Sent(t, "head", head.Bucket, head.Key, head.VersionId, "version-1")
		}
		if c.deleteIn != nil {
			wasabiR5Sent(t, "delete", c.deleteIn.Bucket, c.deleteIn.Key, c.deleteIn.VersionId, "version-1")
		}
	}
	wasabiR5Run(t, "storage.object.delete", []wasabiR5Case{
		{name: "already absent", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(wasabiR5Head{err: wasabiR5Coded("NoSuchVersion")}, gone),
			calls: "HeadObject", output: wasabiR5Deleted(t, "absent"), check: sent},
		{name: "inspection fails", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(wasabiR5Head{err: wasabiR5Coded("AccessDenied")}, gone),
			calls: "HeadObject", want: "Wasabi object could not be inspected before deletion"},
		{name: "version not confirmed", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(wasabiR5Present(nil, nil), gone),
			calls: "HeadObject", want: wasabiR5Unconfirmed},
		{name: "version differs", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(wasabiR5Present(nil, aws.String("version-2")), gone),
			calls: "HeadObject", want: wasabiR5Unconfirmed},
		{name: "etag differs", input: wasabiR5DeleteETag("\"other\""), script: wasabiR5DeleteScript(present, gone),
			calls: "HeadObject", want: "object ETag does not match; refusing to delete it"},
		{name: "etag missing", input: wasabiR5DeleteETag("\"etag\""), script: wasabiR5DeleteScript(wasabiR5Present(nil, aws.String("version-1")), gone),
			calls: "HeadObject", want: "object ETag does not match; refusing to delete it"},
		{name: "etag matches", input: wasabiR5DeleteETag("\"etag\""), script: wasabiR5DeleteScript(present, gone),
			calls: wasabiR5DeleteCalls, output: wasabiR5Deleted(t, "absent"), check: sent},
		{name: "no etag guard", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(wasabiR5Present(nil, aws.String("version-1")), gone),
			calls: wasabiR5DeleteCalls, output: wasabiR5Deleted(t, "absent"), check: sent},
		{name: "deletion fails", input: wasabiR5Delete(nil), calls: "HeadObject DeleteObject", want: "Wasabi object deletion failed", sideEffect: true,
			script: func(c *wasabiR5Client) {
				wasabiR5DeleteScript(present, gone)(c)
				c.deleteErr = wasabiR5Coded("InternalError")
			}, check: sent},
		{name: "still present", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(present, present),
			calls: wasabiR5DeleteCalls, output: wasabiR5Deleted(t, "present"), check: sent},
		{name: "afterwards no such key", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(present, wasabiR5Head{err: wasabiR5Coded("NoSuchKey")}),
			calls: wasabiR5DeleteCalls, output: wasabiR5Deleted(t, "absent")},
		{name: "afterwards unknown", input: wasabiR5Delete(nil), script: wasabiR5DeleteScript(present, wasabiR5Head{err: wasabiR5Coded("AccessDenied")}),
			calls: wasabiR5DeleteCalls, output: wasabiR5Deleted(t, "unknown"), check: sent},
	})
}
