package wasabistorage

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Characterization tests for storage.object.list: request validation,
// continuation-token checks, the ListObjectsV2 request, item validation and
// the listing state.

const (
	wasabiR5ListInvalid = "invalid object list request"
	wasabiR5ListRefused = "continuation token is not valid for this container and prefix"
	wasabiR5ListOutside = "Wasabi listing returned an object outside the authorized prefix"
)

func wasabiR5List(mutate func(*listInput)) listInput {
	value := listInput{ContainerRef: testContainer(), Prefix: "server/", MaximumItems: 2}
	storageR3Apply(mutate, &value)
	return value
}

func wasabiR5ListToken(token string) listInput {
	return wasabiR5List(func(in *listInput) { in.ContinuationToken = token })
}

func wasabiR5Listed(objects ...types.Object) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) { c.listOut = &s3.ListObjectsV2Output{Contents: objects} }
}

func wasabiR5Truncated(next *string) func(*wasabiR5Client) {
	return func(c *wasabiR5Client) {
		c.listOut = &s3.ListObjectsV2Output{IsTruncated: aws.Bool(true), NextContinuationToken: next}
	}
}

func wasabiR5Object(key string, size *int64) types.Object {
	return types.Object{Key: aws.String(key), Size: size}
}

// wasabiR5Page is the expected output of a page with the given items.
func wasabiR5Page(t *testing.T, state string, next any, items ...map[string]any) string {
	t.Helper()
	if items == nil {
		items = []map[string]any{}
	}
	return wasabiR5Output(t, map[string]any{"container_ref": testContainer(), "items": items, "listing_state": state, "next_continuation_token": next})
}

func wasabiR5Item(key string, size int64, extra map[string]any) map[string]any {
	item := map[string]any{"object_ref": testRef(key, ""), "size_bytes": size}
	for name, value := range extra {
		item[name] = value
	}
	return item
}

func TestWasabiR5ListRejectsBeforeProvider(t *testing.T) {
	signed := func(environment, container, prefix, provider string) listInput {
		return wasabiR5ListToken(wasabiR5Sign(wasabiR5Payload(environment, container, prefix, provider)))
	}
	valid := wasabiR5Sign(wasabiR5Payload("staging", "backups", "server/", "p1"))
	payload, mac, _ := strings.Cut(valid, ".")
	wasabiR5Run(t, "storage.object.list", []wasabiR5Case{
		{name: "malformed input", input: wasabiR5List(nil), mutate: wasabiR5BadJSON, want: wasabiR5ListInvalid},
		{name: "request environment", input: wasabiR5List(nil), mutate: wasabiR5WrongEnvironment, want: wasabiR5ListInvalid},
		{name: "container adapter", input: wasabiR5List(func(in *listInput) { in.ContainerRef.AdapterID = "storage.other" }), want: wasabiR5ListInvalid},
		{name: "container profile", input: wasabiR5List(func(in *listInput) { in.ContainerRef.StorageProfile = "other" }), want: wasabiR5ListInvalid},
		{name: "container environment", input: wasabiR5List(func(in *listInput) { in.ContainerRef.EnvironmentID = "production" }), want: wasabiR5ListInvalid},
		{name: "container bucket", input: wasabiR5List(func(in *listInput) { in.ContainerRef.Container = "other" }), want: wasabiR5ListInvalid},
		{name: "prefix outside profile", input: wasabiR5List(func(in *listInput) { in.Prefix = "other/" }), want: wasabiR5ListInvalid},
		{name: "prefix too long", input: wasabiR5List(func(in *listInput) { in.Prefix = "server/" + strings.Repeat("k", 2042) }), want: wasabiR5ListInvalid},
		{name: "zero items", input: wasabiR5List(func(in *listInput) { in.MaximumItems = 0 }), want: wasabiR5ListInvalid},
		{name: "too many items", input: wasabiR5List(func(in *listInput) { in.MaximumItems = 1001 }), want: wasabiR5ListInvalid},
		{name: "invalid before token", input: wasabiR5List(func(in *listInput) { in.MaximumItems = 0; in.ContinuationToken = "x" }), want: wasabiR5ListInvalid},
		{name: "token without separator", input: wasabiR5ListToken("token"), want: wasabiR5ListRefused},
		{name: "token payload encoding", input: wasabiR5ListToken("!!." + mac), want: wasabiR5ListRefused},
		{name: "token signature encoding", input: wasabiR5ListToken(payload + ".!!"), want: wasabiR5ListRefused},
		{name: "token signature", input: wasabiR5ListToken(payload + "." + strings.Repeat("A", len(mac))), want: wasabiR5ListRefused},
		{name: "token json", input: wasabiR5ListToken(wasabiR5Sign("not json")), want: wasabiR5ListRefused},
		{name: "token environment", input: signed("production", "backups", "server/", "p1"), want: wasabiR5ListRefused},
		{name: "token container", input: signed("staging", "other", "server/", "p1"), want: wasabiR5ListRefused},
		{name: "token prefix", input: signed("staging", "backups", "server/a", "p1"), want: wasabiR5ListRefused},
		{name: "token provider empty", input: signed("staging", "backups", "server/", ""), want: wasabiR5ListRefused},
		{name: "token before credentials", input: wasabiR5ListToken("token"), mutate: wasabiR5NoSecrets, want: wasabiR5ListRefused},
		{name: "credentials", input: wasabiR5List(nil), mutate: wasabiR5NoSecrets, want: wasabiR5Credentials},
	})
}

func TestWasabiR5ListRequest(t *testing.T) {
	sent := func(prefix string, items int32, token string) func(*testing.T, *wasabiR5Client) {
		return func(t *testing.T, c *wasabiR5Client) {
			storageR3Equal(t, "bucket", aws.ToString(c.listIn.Bucket), "backups")
			storageR3Equal(t, "prefix", aws.ToString(c.listIn.Prefix), prefix)
			storageR3Equal(t, "max keys", aws.ToInt32(c.listIn.MaxKeys), items)
			storageR3Equal(t, "token set", c.listIn.ContinuationToken != nil, token != "")
			storageR3Equal(t, "token", aws.ToString(c.listIn.ContinuationToken), token)
		}
	}
	long := "server/" + strings.Repeat("k", 2041)
	wasabiR5Run(t, "storage.object.list", []wasabiR5Case{
		{name: "no token", input: wasabiR5List(nil), script: wasabiR5Listed(), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "complete", nil), check: sent("server/", 2, "")},
		{name: "bound token", input: wasabiR5ListToken(wasabiR5Sign(wasabiR5Payload("staging", "backups", "server/", "p1"))),
			script: wasabiR5Listed(), calls: "ListObjectsV2", output: wasabiR5Page(t, "complete", nil), check: sent("server/", 2, "p1")},
		{name: "prefix at limit", input: wasabiR5List(func(in *listInput) { in.Prefix = long }), script: wasabiR5Listed(),
			calls: "ListObjectsV2", output: wasabiR5Page(t, "complete", nil), check: sent(long, 2, "")},
		{name: "one item", input: wasabiR5List(func(in *listInput) { in.MaximumItems = 1 }), script: wasabiR5Listed(),
			calls: "ListObjectsV2", output: wasabiR5Page(t, "complete", nil), check: sent("server/", 1, "")},
		{name: "item limit", input: wasabiR5List(func(in *listInput) { in.MaximumItems = 1000 }), script: wasabiR5Listed(),
			calls: "ListObjectsV2", output: wasabiR5Page(t, "complete", nil), check: sent("server/", 1000, "")},
		{name: "provider failure", input: wasabiR5List(nil), script: func(c *wasabiR5Client) { c.listErr = wasabiR5Coded("AccessDenied") },
			calls: "ListObjectsV2", want: "Wasabi object listing failed"},
	})
}

func TestWasabiR5ListItems(t *testing.T) {
	modified := time.Date(2026, 3, 4, 5, 6, 7, 891, time.FixedZone("plus2", 2*3600))
	tagged := types.Object{Key: aws.String("server/a"), Size: aws.Int64(3), ETag: aws.String("\"etag-a\""), LastModified: &modified}
	blank := types.Object{Key: aws.String("server/b"), Size: aws.Int64(0), ETag: aws.String("")}
	longKey := "server/" + strings.Repeat("k", 2041)
	wasabiR5Run(t, "storage.object.list", []wasabiR5Case{
		{name: "metadata", input: wasabiR5List(nil), script: wasabiR5Listed(tagged, blank), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "complete", nil,
				wasabiR5Item("server/a", 3, map[string]any{"provider_etag": "\"etag-a\"", "last_modified": "2026-03-04T03:06:07.000000891Z"}),
				wasabiR5Item("server/b", 0, nil))},
		{name: "no etag", input: wasabiR5List(nil), script: wasabiR5Listed(wasabiR5Object("server/c", aws.Int64(1))), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "complete", nil, wasabiR5Item("server/c", 1, nil))},
		{name: "key at limit", input: wasabiR5List(nil), script: wasabiR5Listed(wasabiR5Object(longKey, aws.Int64(1))), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "complete", nil, wasabiR5Item(longKey, 1, nil))},
		{name: "outside prefix", input: wasabiR5List(func(in *listInput) { in.Prefix = "server/a/" }),
			script: wasabiR5Listed(wasabiR5Object("server/b", aws.Int64(1))), calls: "ListObjectsV2", want: wasabiR5ListOutside},
		{name: "key too long", input: wasabiR5List(nil), script: wasabiR5Listed(wasabiR5Object(longKey+"k", aws.Int64(1))),
			calls: "ListObjectsV2", want: wasabiR5ListOutside},
		{name: "missing size", input: wasabiR5List(nil), script: wasabiR5Listed(wasabiR5Object("server/a", nil)),
			calls: "ListObjectsV2", want: wasabiR5ListOutside},
		{name: "negative size", input: wasabiR5List(nil), script: wasabiR5Listed(wasabiR5Object("server/a", aws.Int64(-1))),
			calls: "ListObjectsV2", want: wasabiR5ListOutside},
		{name: "page too large", input: wasabiR5List(nil), calls: "ListObjectsV2", want: "Wasabi listing exceeded the requested page size",
			script: wasabiR5Listed(wasabiR5Object("server/a", aws.Int64(1)), wasabiR5Object("server/b", aws.Int64(1)), wasabiR5Object("server/c", aws.Int64(1)))},
	})
}

func TestWasabiR5ListState(t *testing.T) {
	// A payload of 1467 bytes encodes to exactly 2000 token characters.
	fits := strings.Repeat("t", 1417)
	fitsToken := wasabiR5Sign(wasabiR5Payload("staging", "backups", "server/", fits))
	storageR3Equal(t, "token at limit", len(fitsToken), maxContinuationToken)
	wasabiR5Run(t, "storage.object.list", []wasabiR5Case{
		{name: "not truncated", input: wasabiR5List(nil), calls: "ListObjectsV2", output: wasabiR5Page(t, "complete", nil),
			script: func(c *wasabiR5Client) { c.listOut = &s3.ListObjectsV2Output{IsTruncated: aws.Bool(false)} }},
		{name: "truncated", input: wasabiR5List(nil), script: wasabiR5Truncated(aws.String("p1")), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "truncated", wasabiR5Sign(wasabiR5Payload("staging", "backups", "server/", "p1")))},
		{name: "truncated without token", input: wasabiR5List(nil), script: wasabiR5Truncated(nil), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "unknown", nil)},
		{name: "truncated with empty token", input: wasabiR5List(nil), script: wasabiR5Truncated(aws.String("")), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "unknown", nil)},
		{name: "token at limit", input: wasabiR5List(nil), script: wasabiR5Truncated(aws.String(fits)), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "truncated", fitsToken)},
		{name: "token too long", input: wasabiR5List(nil), script: wasabiR5Truncated(aws.String(fits + "t")), calls: "ListObjectsV2",
			output: wasabiR5Page(t, "unknown", nil)},
	})
}

// TestWasabiR5ListTokenRoundTrip pins that a token this adapter issued is
// accepted for the same prefix and refused by another adapter instance.
func TestWasabiR5ListTokenRoundTrip(t *testing.T) {
	impl := wasabiR5Adapter(t, testConfig(), &wasabiR5Client{})
	token := impl.sealListToken("p1", "server/")
	storageR3Equal(t, "sealed token", token, wasabiR5Sign(wasabiR5Payload("staging", "backups", "server/", "p1")))
	provider, ok := impl.openListToken(token, "server/")
	storageR3Equal(t, "opened", ok, true)
	storageR3Equal(t, "provider token", provider, "p1")
	other := wasabiR5Adapter(t, testConfig(), &wasabiR5Client{})
	other.tokenKey = [32]byte{9}
	_, ok = other.openListToken(token, "server/")
	storageR3Equal(t, "opened by another instance", ok, false)
}
