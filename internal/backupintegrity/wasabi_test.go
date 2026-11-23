package backupintegrity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeGetter struct {
	content []byte
	length  int64
	failure error
	calls   int
	bucket  string
	key     string
	version string
}

func (f *fakeGetter) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.calls++
	f.bucket, f.key = aws.ToString(input.Bucket), aws.ToString(input.Key)
	f.version = aws.ToString(input.VersionId)
	if f.failure != nil {
		return nil, f.failure
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(f.content)), ContentLength: aws.Int64(f.length),
	}, nil
}

func remoteRef() json.RawMessage {
	return json.RawMessage(`{"adapter_id":"storage.wasabi","storage_profile_id":"backup-profile","environment_id":"staging","container":"backup-bucket","key":"server/backup-id/world","version_id":"version-123"}`)
}

func readerForTest(t *testing.T, getter Getter, maxBytes int64) *WasabiReader {
	t.Helper()
	r, err := NewWasabiReader(getter, "storage.wasabi", "backup-profile", "staging", "backup-bucket", maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFullWasabiObjectReadbackAndVersionPin(t *testing.T) {
	blob := []byte("isolated staging game world")
	getter := &fakeGetter{content: blob, length: int64(len(blob))}
	reading, err := readerForTest(t, getter, 1024).Readback(context.Background(), remoteRef())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(blob)
	if !reading.Remote || reading.SizeBytes != int64(len(blob)) || reading.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("readback = %#v", reading)
	}
	if getter.calls != 1 || getter.bucket != "backup-bucket" || getter.version != "version-123" {
		t.Fatalf("readback was not pinned to exact remote version: %#v", getter)
	}
}

func TestWasabiReadbackFailsClosed(t *testing.T) {
	blob := []byte("Minecraft backup test")
	for _, tt := range []struct {
		name    string
		getter  *fakeGetter
		max     int64
		rewrite string
	}{
		{"too large", &fakeGetter{content: blob, length: int64(len(blob))}, 4, ""},
		{"truncated", &fakeGetter{content: blob, length: 1024}, 2048, ""},
		{"remote missing", &fakeGetter{failure: errors.New("missing object")}, 2048, ""},
		{"wrong profile", &fakeGetter{content: blob, length: int64(len(blob))}, 2048, "other-profile"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref := remoteRef()
			if tt.rewrite != "" {
				ref = json.RawMessage(strings.ReplaceAll(string(ref), "backup-profile", tt.rewrite))
			}
			_, err := readerForTest(t, tt.getter, tt.max).Readback(context.Background(), ref)
			if err == nil {
				t.Fatal("unverified remote object was accepted")
			}
			if tt.rewrite != "" && tt.getter.calls != 0 {
				t.Fatal("mismatched profile must be rejected before network access")
			}
		})
	}
}
