package wasabistorage

import (
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Inputs and shared checks for the list, copy, delete, versioning and
// retention operations. Each operation lives in its own operations_*.go file.

const (
	maxListItems          = 1000
	maxContinuationToken  = 2000
	maxServerSideCopySize = int64(5 * 1024 * 1024 * 1024)
	// maxKeyLength bounds a listing prefix and every key a listing reports.
	maxKeyLength = 2048
)

type containerReference struct {
	AdapterID      string `json:"adapter_id"`
	StorageProfile string `json:"storage_profile_id"`
	EnvironmentID  string `json:"environment_id"`
	Container      string `json:"container"`
}

type ListInput struct {
	ContainerRef      containerReference `json:"container_ref"`
	Prefix            string             `json:"prefix"`
	ContinuationToken string             `json:"continuation_token,omitempty"`
	MaximumItems      int32              `json:"maximum_items"`
}

type CopyInput struct {
	SourceRef         objectReference `json:"source_ref"`
	Destination       objectTarget    `json:"destination"`
	ExpectedSizeBytes int64           `json:"expected_size_bytes"`
	ExpectedChecksum  Checksum        `json:"expected_checksum"`
}

type DeleteInput struct {
	ObjectRef   objectReference `json:"object_ref"`
	IfMatchETag string          `json:"if_match_etag,omitempty"`
}

type VersioningInput struct {
	ContainerRef containerReference `json:"container_ref"`
}

type RetentionInput struct {
	ObjectRef objectReference `json:"object_ref"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (a *Adapter) validContainer(value containerReference) bool {
	return value.AdapterID == a.config.AdapterID && value.StorageProfile == a.config.StorageProfileID &&
		value.EnvironmentID == a.config.EnvironmentID && value.Container == a.config.Container
}

// acceptsContainer reports a request from this profile's environment for
// this profile's container.
func (a *Adapter) acceptsContainer(request adapter.InvokeRequest, ref containerReference) bool {
	return a.validRequestEnvironment(request) && a.validContainer(ref)
}

// acceptsObject reports a request from this profile's environment for an
// object inside its bucket and key prefix.
func (a *Adapter) acceptsObject(request adapter.InvokeRequest, ref objectReference) bool {
	return a.validRequestEnvironment(request) && a.validReference(ref)
}

// refusedVersion reports a version an operation cannot use: the mutable
// "null" version always, and no version when the profile requires one.
func (a *Adapter) refusedVersion(version string) bool {
	return a.missingRequiredVersion(version) || version == "null"
}

// inputRules are a capability's acceptance check, its rejection text, and
// the checks that follow a well-formed input.
type inputRules[T any] struct {
	rejection string
	accept    func(T) bool
	check     func(T) error
}

// decodeChecked decodes an input like decodeInput, then applies the
// capability's further checks in order.
func decodeChecked[T any](request adapter.InvokeRequest, rules inputRules[T]) (T, error) {
	input, err := decodeInput(request, rules.rejection, rules.accept)
	if err != nil {
		return input, err
	}
	if err := rules.check(input); err != nil {
		var refused T
		return refused, err
	}
	return input, nil
}

// headRequest inspects one key in this profile's bucket, at a version when
// one is given.
func (a *Adapter) headRequest(key, version string) *s3.HeadObjectInput {
	return &s3.HeadObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(key), VersionId: optionalVersion(version)}
}
