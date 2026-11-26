package fakeadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

var ObjectCounter int64

// WasabiSimulator simulates Wasabi S3 storage for testing.
type WasabiSimulator struct {
	buckets  map[string]*BucketState
	fail     bool
}

// BucketState tracks a simulated Wasabi bucket.
type BucketState struct {
	Name       string
	ObjectCount int64
	TOTALSize   int64 // bytes
	CreatedAt   int64
	Objects     []string
}

// NewWasabiSimulator creates a new Wasabi storage simulator.
func NewWasabiSimulator() *WasabiSimulator {
	return &WasabiSimulator{
		buckets: make(map[string]*BucketState),
	}
}

// Capabilities returns advertised capabilities.
func (w *WasabiSimulator) Capabilities() []string {
	return []string{"storage.create_bucket", "storage.delete_object", "storage.list_objects"}
}

// CreateBucket simulates creating a Wasabi bucket.
func (w *WasabiSimulator) CreateBucket(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	name := ""
	if params != nil {
		var m map[string]any
		json.Unmarshal(params, &m)
		if nameVal, ok := m["name"].(string); ok {
			name = nameVal
		} else {
			name = fmt.Sprintf("bucket-%d", atomic.AddInt64(&ObjectCounter, 1))
		}
	}

	bucket := &BucketState{
		Name:      name,
		ObjectCount: 0,
	}
	w.buckets[name] = bucket

	data, _ := json.Marshal(map[string]string{
		"status":   "success",
		"bucket":   bucket.Name,
		"endpoint": fmt.Sprintf("https://%s.s3.wasabisys.com", bucket.Name),
	})
	return json.RawMessage(data), nil
}

// DeleteObject simulates deleting an object from a Wasabi bucket.
func (w *WasabiSimulator) DeleteObject(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	data, _ := json.Marshal(map[string]string{
		"status": "success",
	})
	return json.RawMessage(data), nil
}

// PutObject simulates uploading an object to a Wasabi bucket.
func (w *WasabiSimulator) PutObject(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	bucketName := ""
	size := int64(0)

	if bucketVal, ok := input["bucket"].(string); ok {
		bucketName = bucketVal
	}
	if sizeVal, ok := input["size"].(float64); ok {
		size = int64(sizeVal)
	}

	if _, exists := w.buckets[bucketName]; !exists {
		return json.RawMessage(`{"status": "error", "message": "bucket not found"}`), nil
	}

	bucket := w.buckets[bucketName]
	bucket.ObjectCount++
	bucket.TOTALSize += size

	data, _ := json.Marshal(map[string]string{
		"status": "success",
	})
	return json.RawMessage(data), nil
}
