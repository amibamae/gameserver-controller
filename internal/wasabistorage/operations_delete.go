package wasabistorage

import (
	"context"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// deleteObject removes exactly one object version. It never deletes without a
// version ID, because an unversioned delete could affect a different object.
func (a *Adapter) deleteObject(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := a.decodeDelete(request)
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	head := a.headRequest(input.ObjectRef.Key, input.ObjectRef.VersionID)
	current, err := client.HeadObject(ctx, head)
	if isMissing(err) {
		return deleteOutput(input.ObjectRef, "absent"), nil
	}
	if err != nil {
		return nil, errors.New("Wasabi object could not be inspected before deletion")
	}
	if err := deletable(input, current); err != nil {
		return nil, err
	}
	if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.ObjectRef.Key),
		VersionId: aws.String(input.ObjectRef.VersionID)}); err != nil {
		return nil, sideEffectError("Wasabi object deletion failed")
	}
	return deleteOutput(input.ObjectRef, stateAfterDelete(ctx, client, head)), nil
}

func (a *Adapter) decodeDelete(request adapter.InvokeRequest) (deleteInput, error) {
	return decodeChecked(request, inputRules[deleteInput]{
		rejection: "invalid object delete request",
		accept:    func(in deleteInput) bool { return a.acceptsObject(request, in.ObjectRef) },
		check:     exactDelete,
	})
}

func exactDelete(input deleteInput) error {
	if !immutableVersion(input.ObjectRef.VersionID) {
		return errors.New("deletion requires an exact object version")
	}
	return nil
}

// deletable confirms the inspected version and, when asked, its ETag.
func deletable(input deleteInput, current *s3.HeadObjectOutput) error {
	if _, err := confirmedVersion(input.ObjectRef.VersionID, current.VersionId); err != nil {
		return err
	}
	if input.IfMatchETag != "" && aws.ToString(current.ETag) != input.IfMatchETag {
		return errors.New("object ETag does not match; refusing to delete it")
	}
	return nil
}

// stateAfterDelete inspects the version again: "present", "absent", or
// "unknown" when the inspection fails.
func stateAfterDelete(ctx context.Context, client s3Client, head *s3.HeadObjectInput) string {
	_, err := client.HeadObject(ctx, head)
	if err == nil {
		return "present"
	}
	if isMissing(err) {
		return "absent"
	}
	return "unknown"
}

func deleteOutput(ref objectReference, state string) map[string]any {
	return map[string]any{"object_ref": ref, "state": state, "observed_at": now()}
}
