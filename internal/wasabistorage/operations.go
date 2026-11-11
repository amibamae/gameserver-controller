package wasabistorage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	maxListItems          = 1000
	maxContinuationToken  = 2000
	maxServerSideCopySize = int64(5 * 1024 * 1024 * 1024)
)

type containerReference struct {
	AdapterID      string `json:"adapter_id"`
	StorageProfile string `json:"storage_profile_id"`
	EnvironmentID  string `json:"environment_id"`
	Container      string `json:"container"`
}

type listInput struct {
	ContainerRef      containerReference `json:"container_ref"`
	Prefix            string             `json:"prefix"`
	ContinuationToken string             `json:"continuation_token,omitempty"`
	MaximumItems      int32              `json:"maximum_items"`
}

type copyInput struct {
	SourceRef         objectReference `json:"source_ref"`
	Destination       objectTarget    `json:"destination"`
	ExpectedSizeBytes int64           `json:"expected_size_bytes"`
	ExpectedChecksum  checksum        `json:"expected_checksum"`
}

type deleteInput struct {
	ObjectRef   objectReference `json:"object_ref"`
	IfMatchETag string          `json:"if_match_etag,omitempty"`
}

type versioningInput struct {
	ContainerRef containerReference `json:"container_ref"`
}

type retentionInput struct {
	ObjectRef objectReference `json:"object_ref"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (a *Adapter) validContainer(value containerReference) bool {
	return value.AdapterID == a.config.AdapterID && value.StorageProfile == a.config.StorageProfileID &&
		value.EnvironmentID == a.config.EnvironmentID && value.Container == a.config.Container
}

// exactVersion reports whether a reference names one immutable provider version.
func exactVersion(ref objectReference) bool {
	return ref.VersionID != "" && ref.VersionID != "null"
}

// list returns one bounded page of current objects under the profile's key prefix.
// Continuation tokens are wrapped in an HMAC bound to this adapter instance,
// container, environment, and prefix, so a token cannot be replayed elsewhere.
func (a *Adapter) list(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input listInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validContainer(input.ContainerRef) ||
		!strings.HasPrefix(input.Prefix, a.config.KeyPrefix) || len(input.Prefix) > 2048 || input.MaximumItems < 1 || input.MaximumItems > maxListItems {
		return nil, errors.New("invalid object list request")
	}
	providerToken := ""
	if input.ContinuationToken != "" {
		token, ok := a.openListToken(input.ContinuationToken, input.Prefix)
		if !ok {
			return nil, errors.New("continuation token is not valid for this container and prefix")
		}
		providerToken = token
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	list := &s3.ListObjectsV2Input{Bucket: aws.String(a.config.Container), Prefix: aws.String(input.Prefix), MaxKeys: aws.Int32(input.MaximumItems)}
	if providerToken != "" {
		list.ContinuationToken = aws.String(providerToken)
	}
	result, err := client.ListObjectsV2(ctx, list)
	if err != nil {
		return nil, errors.New("Wasabi object listing failed")
	}
	items := make([]map[string]any, 0, len(result.Contents))
	for _, object := range result.Contents {
		key := aws.ToString(object.Key)
		// Never report keys outside the requested prefix, even if the provider does.
		if !strings.HasPrefix(key, input.Prefix) || key == "" || len(key) > 2048 || object.Size == nil || *object.Size < 0 {
			return nil, errors.New("Wasabi listing returned an object outside the authorized prefix")
		}
		item := map[string]any{"object_ref": objectReference{objectTarget: objectTarget{AdapterID: a.config.AdapterID,
			StorageProfile: a.config.StorageProfileID, EnvironmentID: a.config.EnvironmentID, Container: a.config.Container, Key: key}},
			"size_bytes": *object.Size}
		if etag := aws.ToString(object.ETag); etag != "" {
			item["provider_etag"] = etag
		}
		if object.LastModified != nil {
			item["last_modified"] = object.LastModified.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	if len(items) > int(input.MaximumItems) {
		return nil, errors.New("Wasabi listing exceeded the requested page size")
	}
	value := map[string]any{"container_ref": input.ContainerRef, "items": items, "listing_state": "complete",
		"next_continuation_token": nil, "observed_at": now()}
	if aws.ToBool(result.IsTruncated) {
		next := aws.ToString(result.NextContinuationToken)
		token := ""
		if next != "" {
			token = a.sealListToken(next, input.Prefix)
		}
		if token == "" || len(token) > maxContinuationToken {
			value["listing_state"] = "unknown"
		} else {
			value["listing_state"] = "truncated"
			value["next_continuation_token"] = token
		}
	}
	return value, nil
}

type listTokenPayload struct {
	Environment string `json:"e"`
	Container   string `json:"c"`
	Prefix      string `json:"p"`
	Provider    string `json:"t"`
}

func (a *Adapter) sealListToken(providerToken, prefix string) string {
	payload, err := json.Marshal(listTokenPayload{Environment: a.config.EnvironmentID, Container: a.config.Container, Prefix: prefix, Provider: providerToken})
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, a.tokenKey[:])
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Adapter) openListToken(token, prefix string) (string, bool) {
	encodedPayload, encodedMAC, found := strings.Cut(token, ".")
	if !found {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return "", false
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, a.tokenKey[:])
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", false
	}
	var value listTokenPayload
	if json.Unmarshal(payload, &value) != nil || value.Environment != a.config.EnvironmentID || value.Container != a.config.Container || value.Prefix != prefix || value.Provider == "" {
		return "", false
	}
	return value.Provider, true
}

// copyObject performs a server-side copy within this profile and reports
// "copied" only after the new object version is read back and matches the
// expected size and SHA-256. It refuses to overwrite an existing destination.
func (a *Adapter) copyObject(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input copyInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validReference(input.SourceRef) ||
		!a.validTarget(input.Destination) || !validExpected(input.ExpectedSizeBytes, input.ExpectedChecksum, a.config.MaxObjectBytes) {
		return nil, errors.New("invalid object copy request")
	}
	if !exactVersion(input.SourceRef) && (a.config.RequireVersionIDs || input.SourceRef.VersionID == "null") {
		return nil, errors.New("an exact source object version is required by this storage profile")
	}
	if input.SourceRef.Key == input.Destination.Key {
		return nil, errors.New("copy destination must differ from its source")
	}
	if input.ExpectedSizeBytes > maxServerSideCopySize {
		return nil, errors.New("object is larger than the single-request server-side copy limit")
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	source := &s3.HeadObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.SourceRef.Key)}
	if input.SourceRef.VersionID != "" {
		source.VersionId = aws.String(input.SourceRef.VersionID)
	}
	sourceHead, err := client.HeadObject(ctx, source)
	if err != nil {
		return nil, errors.New("Wasabi copy source could not be inspected")
	}
	if _, err := confirmedVersion(input.SourceRef.VersionID, sourceHead.VersionId); err != nil {
		return nil, err
	}
	if sourceHead.ContentLength == nil || *sourceHead.ContentLength != input.ExpectedSizeBytes {
		return nil, errors.New("Wasabi copy source size does not match the expected size")
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.Destination.Key)}); err == nil {
		return nil, errors.New("copy destination already exists; refusing to overwrite it")
	} else if !isMissing(err) {
		return nil, errors.New("Wasabi copy destination could not be inspected")
	}
	copySource := a.config.Container + "/" + escapeKey(input.SourceRef.Key)
	if input.SourceRef.VersionID != "" {
		copySource += "?versionId=" + url.QueryEscape(input.SourceRef.VersionID)
	}
	result, err := client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.Destination.Key),
		CopySource: aws.String(copySource), ChecksumAlgorithm: types.ChecksumAlgorithmSha256})
	if err != nil {
		return nil, sideEffectError("Wasabi server-side copy failed")
	}
	ref := objectReference{objectTarget: input.Destination}
	ref.VersionID, _ = confirmedVersion("", result.VersionId)
	if a.config.RequireVersionIDs && !exactVersion(ref) {
		return nil, sideEffectError("Wasabi did not return the required immutable version for the copy")
	}
	readback := &s3.GetObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.Destination.Key)}
	if ref.VersionID != "" {
		readback.VersionId = aws.String(ref.VersionID)
	}
	body, err := client.GetObject(ctx, readback)
	if err != nil {
		return nil, sideEffectError("Wasabi copy could not be read back for verification")
	}
	defer body.Body.Close()
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(body.Body, input.ExpectedSizeBytes+1))
	if err != nil || count != input.ExpectedSizeBytes || !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), input.ExpectedChecksum.Digest) {
		return nil, sideEffectError("Wasabi copy does not match the expected size and checksum")
	}
	value := map[string]any{"destination": input.Destination, "copy_state": "copied", "object_ref": ref, "bytes_copied": count, "observed_at": now()}
	if result.CopyObjectResult != nil && aws.ToString(result.CopyObjectResult.ETag) != "" {
		value["provider_etag"] = aws.ToString(result.CopyObjectResult.ETag)
	}
	return value, nil
}

// deleteObject removes exactly one object version. It never deletes without a
// version ID, because an unversioned delete could affect a different object.
func (a *Adapter) deleteObject(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input deleteInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validReference(input.ObjectRef) {
		return nil, errors.New("invalid object delete request")
	}
	if !exactVersion(input.ObjectRef) {
		return nil, errors.New("deletion requires an exact object version")
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	head := &s3.HeadObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.ObjectRef.Key), VersionId: aws.String(input.ObjectRef.VersionID)}
	current, err := client.HeadObject(ctx, head)
	if err != nil {
		if isMissing(err) {
			return map[string]any{"object_ref": input.ObjectRef, "state": "absent", "observed_at": now()}, nil
		}
		return nil, errors.New("Wasabi object could not be inspected before deletion")
	}
	if _, err := confirmedVersion(input.ObjectRef.VersionID, current.VersionId); err != nil {
		return nil, err
	}
	if input.IfMatchETag != "" && aws.ToString(current.ETag) != input.IfMatchETag {
		return nil, errors.New("object ETag does not match; refusing to delete it")
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.ObjectRef.Key),
		VersionId: aws.String(input.ObjectRef.VersionID)}); err != nil {
		return nil, sideEffectError("Wasabi object deletion failed")
	}
	state := "unknown"
	if _, err := client.HeadObject(ctx, head); err == nil {
		state = "present"
	} else if isMissing(err) {
		state = "absent"
	}
	return map[string]any{"object_ref": input.ObjectRef, "state": state, "observed_at": now()}, nil
}

func (a *Adapter) inspectVersioning(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input versioningInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validContainer(input.ContainerRef) {
		return nil, errors.New("invalid versioning inspection request")
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	state := "unknown"
	if result, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(a.config.Container)}); err == nil {
		switch result.Status {
		case types.BucketVersioningStatusEnabled:
			state = "enabled"
		case types.BucketVersioningStatusSuspended:
			state = "suspended"
		case "":
			state = "disabled"
		}
	}
	return map[string]any{"container_ref": input.ContainerRef, "state": state, "observed_at": now()}, nil
}

// inspectRetention reports object-lock retention and legal hold. A missing
// object-lock configuration is reported as none/off; any other failure stays unknown.
func (a *Adapter) inspectRetention(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input retentionInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validReference(input.ObjectRef) {
		return nil, errors.New("invalid retention inspection request")
	}
	if !exactVersion(input.ObjectRef) && (a.config.RequireVersionIDs || input.ObjectRef.VersionID == "null") {
		return nil, errors.New("an exact object version is required by this storage profile")
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	var version *string
	if input.ObjectRef.VersionID != "" {
		version = aws.String(input.ObjectRef.VersionID)
	}
	bucket, key := aws.String(a.config.Container), aws.String(input.ObjectRef.Key)
	value := map[string]any{"object_ref": input.ObjectRef, "state": "observed", "mode": "unknown", "legal_hold": "unknown", "retain_until": nil, "observed_at": now()}
	retention, err := client.GetObjectRetention(ctx, &s3.GetObjectRetentionInput{Bucket: bucket, Key: key, VersionId: version})
	switch {
	case err == nil && retention.Retention != nil:
		switch retention.Retention.Mode {
		case types.ObjectLockRetentionModeGovernance:
			value["mode"] = "governance"
		case types.ObjectLockRetentionModeCompliance:
			value["mode"] = "compliance"
		}
		if retention.Retention.RetainUntilDate != nil {
			value["retain_until"] = retention.Retention.RetainUntilDate.UTC().Format(time.RFC3339Nano)
		}
	case err == nil || noObjectLock(err):
		value["mode"] = "none"
	case isMissing(err):
		return nil, errors.New("object version is absent")
	}
	hold, err := client.GetObjectLegalHold(ctx, &s3.GetObjectLegalHoldInput{Bucket: bucket, Key: key, VersionId: version})
	switch {
	case err == nil && hold.LegalHold != nil && hold.LegalHold.Status == types.ObjectLockLegalHoldStatusOn:
		value["legal_hold"] = "on"
	case err == nil && hold.LegalHold != nil && hold.LegalHold.Status == types.ObjectLockLegalHoldStatusOff:
		value["legal_hold"] = "off"
	case err == nil && hold.LegalHold == nil, err != nil && noObjectLock(err):
		value["legal_hold"] = "off"
	}
	if value["mode"] == "unknown" || value["legal_hold"] == "unknown" {
		value["state"] = "unknown"
	}
	return value, nil
}

func errorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func isMissing(err error) bool {
	switch errorCode(err) {
	case "NotFound", "NoSuchKey", "NoSuchVersion":
		return true
	}
	return false
}

func noObjectLock(err error) bool {
	switch errorCode(err) {
	case "ObjectLockConfigurationNotFoundError", "NoSuchObjectLockConfiguration":
		return true
	}
	return false
}

// escapeKey URL-encodes each key segment for the CopySource header while keeping "/" separators.
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
