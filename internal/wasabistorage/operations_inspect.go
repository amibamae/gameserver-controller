package wasabistorage

import (
	"context"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Bucket versioning and object-lock inspection.

var (
	versioningStates = map[types.BucketVersioningStatus]string{
		types.BucketVersioningStatusEnabled: "enabled", types.BucketVersioningStatusSuspended: "suspended", "": "disabled",
	}
	retentionModes = map[types.ObjectLockRetentionMode]string{
		types.ObjectLockRetentionModeGovernance: "governance", types.ObjectLockRetentionModeCompliance: "compliance",
	}
	legalHoldStates = map[types.ObjectLockLegalHoldStatus]string{
		types.ObjectLockLegalHoldStatusOn: "on", types.ObjectLockLegalHoldStatusOff: "off",
	}
)

func (a *Adapter) inspectVersioning(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := decodeInput(request, "invalid versioning inspection request", func(in versioningInput) bool { return a.acceptsContainer(request, in.ContainerRef) })
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"container_ref": input.ContainerRef, "state": a.versioningState(ctx, client), "observed_at": now()}, nil
}

// versioningState maps the bucket status; a failed call or an unrecognized
// status is "unknown".
func (a *Adapter) versioningState(ctx context.Context, client s3Client) string {
	result, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(a.config.Container)})
	if err != nil {
		return "unknown"
	}
	return knownState(versioningStates, result.Status)
}

// inspectRetention reports object-lock retention and legal hold. A missing
// object-lock configuration is reported as none/off; any other failure stays unknown.
func (a *Adapter) inspectRetention(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := a.decodeRetention(request)
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	bucket, key, version := aws.String(a.config.Container), aws.String(input.ObjectRef.Key), optionalVersion(input.ObjectRef.VersionID)
	value := map[string]any{"object_ref": input.ObjectRef, "state": "observed", "mode": "unknown", "legal_hold": "unknown", "retain_until": nil, "observed_at": now()}
	mode, until, err := retentionState(client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: bucket, Key: key, VersionId: version}))
	if err != nil {
		return nil, err
	}
	hold := legalHoldState(client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: bucket, Key: key, VersionId: version}))
	value["mode"], value["retain_until"], value["legal_hold"] = mode, until, hold
	if mode == "unknown" || hold == "unknown" {
		value["state"] = "unknown"
	}
	return value, nil
}

func (a *Adapter) decodeRetention(request adapter.InvokeRequest) (retentionInput, error) {
	return decodeChecked(request, inputRules[retentionInput]{
		rejection: "invalid retention inspection request",
		accept:    func(in retentionInput) bool { return a.acceptsObject(request, in.ObjectRef) },
		check:     a.retentionVersion,
	})
}

func (a *Adapter) retentionVersion(input retentionInput) error {
	if a.refusedVersion(input.ObjectRef.VersionID) {
		return errors.New("an exact object version is required by this storage profile")
	}
	return nil
}

// retentionState returns the lock mode and retain-until time. A bucket
// without object lock reports "none"; an absent version is an error; any
// other failure leaves the mode "unknown".
func retentionState(result *s3.GetObjectRetentionOutput, err error) (string, any, error) {
	switch {
	case err == nil && result.Retention != nil:
		return knownState(retentionModes, result.Retention.Mode), retainUntil(result.Retention.RetainUntilDate), nil
	case err == nil || noObjectLock(err):
		return "none", nil, nil
	case isMissing(err):
		return "", nil, errors.New("object version is absent")
	}
	return "unknown", nil, nil
}

// retainUntil formats a retain-until time, or returns nil for none.
func retainUntil(until *time.Time) any {
	if until == nil {
		return nil
	}
	return until.UTC().Format(time.RFC3339Nano)
}

// legalHoldState maps the legal hold. No hold, or a bucket without object
// lock, is "off"; any other failure or an unrecognized status is "unknown".
func legalHoldState(result *s3.GetObjectLegalHoldOutput, err error) string {
	if err != nil {
		return lockUnavailableState(err)
	}
	if result.LegalHold == nil {
		return "off"
	}
	return knownState(legalHoldStates, result.LegalHold.Status)
}

func lockUnavailableState(err error) string {
	if noObjectLock(err) {
		return "off"
	}
	return "unknown"
}

// knownState looks up a provider value, or returns "unknown".
func knownState[K comparable](states map[K]string, value K) string {
	if state, ok := states[value]; ok {
		return state
	}
	return "unknown"
}
