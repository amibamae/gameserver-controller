package wasabistorage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// list returns one bounded page of current objects under the profile's key prefix.
// Continuation tokens are wrapped in an HMAC bound to this adapter instance,
// container, environment, and prefix, so a token cannot be replayed elsewhere.
func (a *Adapter) list(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := decodeInput(request, "invalid object list request", func(in listInput) bool { return a.acceptsList(request, in) })
	if err != nil {
		return nil, err
	}
	providerToken, err := a.providerListToken(input)
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	result, err := client.ListObjectsV2(ctx, a.listRequest(input, providerToken))
	if err != nil {
		return nil, errors.New("Wasabi object listing failed")
	}
	items, err := a.listItems(result.Contents, input)
	if err != nil {
		return nil, err
	}
	value := map[string]any{"container_ref": input.ContainerRef, "items": items, "listing_state": "complete",
		"next_continuation_token": nil, "observed_at": now()}
	a.recordTruncation(value, result, input.Prefix)
	return value, nil
}

func (a *Adapter) acceptsList(request adapter.InvokeRequest, input listInput) bool {
	return a.acceptsContainer(request, input.ContainerRef) && a.authorizedListPrefix(input.Prefix) && validPageSize(input.MaximumItems)
}

// authorizedListPrefix reports a bounded prefix inside the profile's key prefix.
func (a *Adapter) authorizedListPrefix(prefix string) bool {
	return strings.HasPrefix(prefix, a.config.KeyPrefix) && len(prefix) <= maxKeyLength
}

func validPageSize(items int32) bool {
	return items >= 1 && items <= maxListItems
}

// providerListToken opens the caller's continuation token, if any, into the
// provider's own token.
func (a *Adapter) providerListToken(input listInput) (string, error) {
	if input.ContinuationToken == "" {
		return "", nil
	}
	token, ok := a.openListToken(input.ContinuationToken, input.Prefix)
	if !ok {
		return "", errors.New("continuation token is not valid for this container and prefix")
	}
	return token, nil
}

func (a *Adapter) listRequest(input listInput, providerToken string) *s3.ListObjectsV2Input {
	list := &s3.ListObjectsV2Input{Bucket: aws.String(a.config.Container), Prefix: aws.String(input.Prefix), MaxKeys: aws.Int32(input.MaximumItems)}
	if providerToken != "" {
		list.ContinuationToken = aws.String(providerToken)
	}
	return list
}

// listItems converts one provider page, refusing any object outside the
// requested prefix and any page larger than requested.
func (a *Adapter) listItems(objects []types.Object, input listInput) ([]map[string]any, error) {
	items := make([]map[string]any, 0, len(objects))
	for _, object := range objects {
		// Never report keys outside the requested prefix, even if the provider does.
		if !listedWithin(object, input.Prefix) {
			return nil, errors.New("Wasabi listing returned an object outside the authorized prefix")
		}
		items = append(items, a.listItem(object))
	}
	if len(items) > int(input.MaximumItems) {
		return nil, errors.New("Wasabi listing exceeded the requested page size")
	}
	return items, nil
}

// listedWithin reports a listed object with a bounded key under prefix and a
// known, non-negative size.
func listedWithin(object types.Object, prefix string) bool {
	return listedKey(aws.ToString(object.Key), prefix) && object.Size != nil && *object.Size >= 0
}

func listedKey(key, prefix string) bool {
	return strings.HasPrefix(key, prefix) && key != "" && len(key) <= maxKeyLength
}

func (a *Adapter) listItem(object types.Object) map[string]any {
	ref := objectReference{objectTarget: objectTarget{AdapterID: a.config.AdapterID, StorageProfile: a.config.StorageProfileID,
		EnvironmentID: a.config.EnvironmentID, Container: a.config.Container, Key: aws.ToString(object.Key)}}
	item := map[string]any{"object_ref": ref, "size_bytes": *object.Size}
	addProviderETag(item, object.ETag)
	if object.LastModified != nil {
		item["last_modified"] = object.LastModified.UTC().Format(time.RFC3339Nano)
	}
	return item
}

// recordTruncation marks a truncated page "truncated" with a sealed next
// token, or "unknown" when no usable token can be issued.
func (a *Adapter) recordTruncation(value map[string]any, result *s3.ListObjectsV2Output, prefix string) {
	if !aws.ToBool(result.IsTruncated) {
		return
	}
	token := a.nextListToken(aws.ToString(result.NextContinuationToken), prefix)
	if token == "" {
		value["listing_state"] = "unknown"
		return
	}
	value["listing_state"] = "truncated"
	value["next_continuation_token"] = token
}

// nextListToken seals the provider's next token, or returns "" when there is
// none or the sealed token would be too long.
func (a *Adapter) nextListToken(next, prefix string) string {
	if next == "" {
		return ""
	}
	token := a.sealListToken(next, prefix)
	if len(token) > maxContinuationToken {
		return ""
	}
	return token
}
