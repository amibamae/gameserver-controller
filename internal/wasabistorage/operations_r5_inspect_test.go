package wasabistorage

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Characterization tests for storage.object.versioning.inspect and
// storage.object.retention.inspect, and for the provider error-code helpers.

const (
	wasabiR5VersioningInvalid = "invalid versioning inspection request"
	wasabiR5RetentionInvalid  = "invalid retention inspection request"
	wasabiR5RetentionVersion  = "an exact object version is required by this storage profile"
	wasabiR5RetentionCalls    = "GetObjectRetention GetObjectLegalHold"
)

func wasabiR5Versioning(mutate func(*containerReference)) VersioningInput {
	return VersioningInput{ContainerRef: wasabiR5Container(mutate)}
}

func wasabiR5Status(status types.BucketVersioningStatus, err error) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) {
		c.versioningOut, c.versioningErr = &s3.GetBucketVersioningOutput{Status: status}, err
	}
}

func wasabiR5Versioned(t *testing.T, state string) string {
	t.Helper()
	return wasabiR5Output(t, map[string]any{"container_ref": testContainer(), "state": state})
}

func TestWasabiR5Versioning(t *testing.T) {
	sent := func(t *testing.T, c *wasabiR5Client) {
		storageR3Equal(t, "bucket", aws.ToString(c.versioningIn.Bucket), "backups")
	}
	wasabiR5Run(t, "storage.object.versioning.inspect", []wasabiR5Case{
		{name: "malformed input", input: wasabiR5Versioning(nil), mutate: wasabiR5BadJSON, want: wasabiR5VersioningInvalid},
		{name: "request environment", input: wasabiR5Versioning(nil), mutate: wasabiR5WrongEnvironment, want: wasabiR5VersioningInvalid},
		{name: "container adapter", input: wasabiR5Versioning(func(c *containerReference) { c.AdapterID = "storage.other" }), want: wasabiR5VersioningInvalid},
		{name: "container bucket", input: wasabiR5Versioning(func(c *containerReference) { c.Container = "other" }), want: wasabiR5VersioningInvalid},
		{name: "credentials", input: wasabiR5Versioning(nil), mutate: wasabiR5NoSecrets, want: wasabiR5Credentials},
		{name: "enabled", input: wasabiR5Versioning(nil), script: wasabiR5Status(types.BucketVersioningStatusEnabled, nil),
			calls: "GetBucketVersioning", output: wasabiR5Versioned(t, "enabled"), check: sent},
		{name: "suspended", input: wasabiR5Versioning(nil), script: wasabiR5Status(types.BucketVersioningStatusSuspended, nil),
			calls: "GetBucketVersioning", output: wasabiR5Versioned(t, "suspended")},
		{name: "never enabled", input: wasabiR5Versioning(nil), script: wasabiR5Status("", nil),
			calls: "GetBucketVersioning", output: wasabiR5Versioned(t, "disabled")},
		{name: "unrecognized status", input: wasabiR5Versioning(nil), script: wasabiR5Status("Paused", nil),
			calls: "GetBucketVersioning", output: wasabiR5Versioned(t, "unknown")},
		{name: "provider failure", input: wasabiR5Versioning(nil), script: wasabiR5Status("", wasabiR5Coded("AccessDenied")),
			calls: "GetBucketVersioning", output: wasabiR5Versioned(t, "unknown")},
	})
}

func wasabiR5Retention(version string) RetentionInput {
	return RetentionInput{ObjectRef: wasabiR5Ref(wasabiR5Version(version))}
}

// wasabiR5Lock scripts both object-lock reads.
func wasabiR5Lock(retention *types.ObjectLockRetention, retentionErr error, hold *types.ObjectLockLegalHold, holdErr error) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) {
		c.retentionOut, c.retentionErr = &s3.GetObjectRetentionOutput{Retention: retention}, retentionErr
		c.holdOut, c.holdErr = &s3.GetObjectLegalHoldOutput{LegalHold: hold}, holdErr
	}
}

func wasabiR5Hold(status types.ObjectLockLegalHoldStatus) *types.ObjectLockLegalHold {
	return &types.ObjectLockLegalHold{Status: status}
}

// wasabiR5Retained is the expected retention result; the state is
// "observed" unless the mode or legal hold is unknown.
func wasabiR5Retained(t *testing.T, version, mode, hold string, until any) string {
	t.Helper()
	state := "observed"
	if mode == "unknown" || hold == "unknown" {
		state = "unknown"
	}
	return wasabiR5Output(t, map[string]any{"object_ref": wasabiR5Ref(wasabiR5Version(version)), "state": state, "mode": mode, "legal_hold": hold, "retain_until": until})
}

func TestWasabiR5RetentionRejectsBeforeProvider(t *testing.T) {
	wasabiR5Run(t, "storage.object.retention.inspect", []wasabiR5Case{
		{name: "malformed input", input: wasabiR5Retention("version-1"), mutate: wasabiR5BadJSON, want: wasabiR5RetentionInvalid},
		{name: "request environment", input: wasabiR5Retention("version-1"), mutate: wasabiR5WrongEnvironment, want: wasabiR5RetentionInvalid},
		{name: "outside prefix", input: RetentionInput{ObjectRef: wasabiR5Ref(wasabiR5OutsideRef)}, want: wasabiR5RetentionInvalid},
		{name: "other bucket", input: RetentionInput{ObjectRef: wasabiR5Ref(wasabiR5OtherBucket)}, want: wasabiR5RetentionInvalid},
		{name: "required version missing", config: wasabiR5RequireVersions, input: wasabiR5Retention(""), want: wasabiR5RetentionVersion},
		{name: "required version null", config: wasabiR5RequireVersions, input: wasabiR5Retention("null"), want: wasabiR5RetentionVersion},
		{name: "null version", input: wasabiR5Retention("null"), want: wasabiR5RetentionVersion},
		{name: "version before credentials", input: wasabiR5Retention("null"), mutate: wasabiR5NoSecrets, want: wasabiR5RetentionVersion},
		{name: "credentials", input: wasabiR5Retention("version-1"), mutate: wasabiR5NoSecrets, want: wasabiR5Credentials},
	})
}

func TestWasabiR5RetentionMode(t *testing.T) {
	until := time.Date(2027, 1, 2, 3, 4, 5, 6, time.FixedZone("minus5", -5*3600))
	const untilText = "2027-01-02T08:04:05.000000006Z"
	on := wasabiR5Hold(types.ObjectLockLegalHoldStatusOn)
	sent := func(version string) func(*testing.T, *wasabiR5Client) {
		return func(t *testing.T, c *wasabiR5Client) {
			wasabiR5Sent(t, "retention", c.retentionIn.Bucket, c.retentionIn.Key, c.retentionIn.VersionId, version)
			wasabiR5Sent(t, "legal hold", c.holdIn.Bucket, c.holdIn.Key, c.holdIn.VersionId, version)
		}
	}
	governance := &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeGovernance, RetainUntilDate: &until}
	absent := "object version is absent"
	wasabiR5Run(t, "storage.object.retention.inspect", []wasabiR5Case{
		{name: "governance", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(governance, nil, on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "governance", "on", untilText), check: sent("version-1")},
		{name: "unversioned", input: wasabiR5Retention(""), script: wasabiR5Lock(governance, nil, on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "", "governance", "on", untilText), check: sent("")},
		{name: "required version present", config: wasabiR5RequireVersions, input: wasabiR5Retention("version-1"), script: wasabiR5Lock(governance, nil, on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "governance", "on", untilText)},
		{name: "compliance without date", input: wasabiR5Retention("version-1"),
			script: wasabiR5Lock(&types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeCompliance}, nil, on, nil),
			calls:  wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "compliance", "on", nil)},
		{name: "unrecognized mode", input: wasabiR5Retention("version-1"),
			script: wasabiR5Lock(&types.ObjectLockRetention{Mode: "LEGACY", RetainUntilDate: &until}, nil, on, nil),
			calls:  wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "unknown", "on", untilText)},
		{name: "no retention", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, nil, on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "on", nil)},
		{name: "no lock configuration", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("ObjectLockConfigurationNotFoundError"), on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "on", nil)},
		{name: "no such lock configuration", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("NoSuchObjectLockConfiguration"), on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "on", nil)},
		{name: "retention failure", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(governance, wasabiR5Coded("AccessDenied"), on, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "unknown", "on", nil)},
		{name: "absent not found", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("NotFound"), on, nil),
			calls: "GetObjectRetention", want: absent},
		{name: "absent no such key", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("NoSuchKey"), on, nil),
			calls: "GetObjectRetention", want: absent},
		{name: "absent no such version", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("NoSuchVersion"), on, nil),
			calls: "GetObjectRetention", want: absent},
	})
}

func TestWasabiR5RetentionLegalHold(t *testing.T) {
	none := func(hold *types.ObjectLockLegalHold, err error) func(*wasabiR5Client) {
		return wasabiR5Lock(nil, nil, hold, err)
	}
	wasabiR5Run(t, "storage.object.retention.inspect", []wasabiR5Case{
		{name: "off", input: wasabiR5Retention("version-1"), script: none(wasabiR5Hold(types.ObjectLockLegalHoldStatusOff), nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "off", nil)},
		{name: "unrecognized status", input: wasabiR5Retention("version-1"), script: none(wasabiR5Hold("PENDING"), nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "unknown", nil)},
		{name: "no legal hold", input: wasabiR5Retention("version-1"), script: none(nil, nil),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "off", nil)},
		{name: "no lock configuration", input: wasabiR5Retention("version-1"), script: none(nil, wasabiR5Coded("ObjectLockConfigurationNotFoundError")),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "off", nil)},
		{name: "no such lock configuration", input: wasabiR5Retention("version-1"), script: none(wasabiR5Hold(types.ObjectLockLegalHoldStatusOn), wasabiR5Coded("NoSuchObjectLockConfiguration")),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "off", nil)},
		{name: "missing version", input: wasabiR5Retention("version-1"), script: none(nil, wasabiR5Coded("NoSuchVersion")),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "unknown", nil)},
		{name: "failure", input: wasabiR5Retention("version-1"), script: none(wasabiR5Hold(types.ObjectLockLegalHoldStatusOn), errors.New("timeout")),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "none", "unknown", nil)},
		{name: "both unknown", input: wasabiR5Retention("version-1"), script: wasabiR5Lock(nil, wasabiR5Coded("AccessDenied"), nil, wasabiR5Coded("AccessDenied")),
			calls: wasabiR5RetentionCalls, output: wasabiR5Retained(t, "version-1", "unknown", "unknown", nil)},
	})
}

func TestWasabiR5ErrorCodes(t *testing.T) {
	for _, c := range []struct {
		err              error
		code             string
		missing, notLock bool
	}{
		{nil, "", false, false},
		{errors.New("NotFound"), "", false, false},
		{wasabiR5Coded("NotFound"), "NotFound", true, false},
		{wasabiR5Coded("NoSuchKey"), "NoSuchKey", true, false},
		{wasabiR5Coded("NoSuchVersion"), "NoSuchVersion", true, false},
		{wasabiR5Coded("ObjectLockConfigurationNotFoundError"), "ObjectLockConfigurationNotFoundError", false, true},
		{codedError("NoSuchObjectLockConfiguration"), "NoSuchObjectLockConfiguration", false, true},
		{wasabiR5Coded("AccessDenied"), "AccessDenied", false, false},
	} {
		storageR3Equal(t, "errorCode", errorCode(c.err), c.code)
		storageR3Equal(t, "isMissing "+c.code, isMissing(c.err), c.missing)
		storageR3Equal(t, "noObjectLock "+c.code, noObjectLock(c.err), c.notLock)
	}
	for key, want := range map[string]string{
		"server/world.tar":    "server/world.tar",
		"server/a b/c+d%.tar": "server/a%20b/c+d%25.tar",
		"server//x?y#z":       "server//x%3Fy%23z",
		"server/café.tar":     "server/caf%C3%A9.tar",
	} {
		storageR3Equal(t, "escapeKey "+key, escapeKey(key), want)
	}
}
