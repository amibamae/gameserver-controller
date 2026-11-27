package adapter

import (
	"errors"
	"io"
	"sync"
)

// OpenStream consumes one controller-provided source stream by its transient
// transfer ID. A source can be claimed once per invocation.
func (c RequestContext) OpenStream(transferID string) (io.ReadCloser, error) {
	if !c.hasStreamAccess(transferID) {
		return nil, errors.New("stream source is unavailable")
	}
	return c.streamAccess.open(transferID)
}

// CreateStream consumes one controller-provided destination stream by its
// transient transfer ID. A destination can be claimed once per invocation.
func (c RequestContext) CreateStream(transferID string) (io.WriteCloser, error) {
	if !c.hasStreamAccess(transferID) {
		return nil, errors.New("stream destination is unavailable")
	}
	return c.streamAccess.create(transferID)
}

// HasStreamDestination reports whether the trusted controller attached a
// destination for this one-use transfer handle. It does not claim the handle.
func (c RequestContext) HasStreamDestination(transferID string) bool {
	if !c.hasStreamAccess(transferID) {
		return false
	}
	c.streamAccess.mu.Lock()
	defer c.streamAccess.mu.Unlock()
	_, ok := c.streamAccess.destinations[transferID]
	return ok && !c.streamAccess.claimedDestinations[transferID]
}

// hasStreamAccess reports whether the controller attached stream handles and
// transferID names one.
func (c RequestContext) hasStreamAccess(transferID string) bool {
	return c.streamAccess != nil && transferID != ""
}

// SetStreamSources attaches invocation-only stream sources. They are excluded
// from JSON and must be set only by the trusted controller runtime.
func (c *RequestContext) SetStreamSources(sources map[string]io.ReadCloser) {
	if c == nil {
		return
	}
	c.streamAccess = &streamAccess{sources: sources, claimedSources: make(map[string]bool)}
}

// SetStreamDestinations attaches invocation-only stream destinations. They
// are excluded from JSON and must be set only by the trusted controller.
func (c *RequestContext) SetStreamDestinations(destinations map[string]io.WriteCloser) {
	if c == nil {
		return
	}
	if c.streamAccess == nil {
		c.streamAccess = &streamAccess{claimedSources: make(map[string]bool)}
	}
	c.streamAccess.destinations = destinations
	c.streamAccess.claimedDestinations = make(map[string]bool)
}

// ClearStreamSources removes invocation-only stream readers and writers before
// a request is retained for diagnostics or test history.
func (c *RequestContext) ClearStreamSources() {
	if c != nil {
		c.streamAccess = nil
	}
}

type streamAccess struct {
	mu                  sync.Mutex
	sources             map[string]io.ReadCloser
	destinations        map[string]io.WriteCloser
	claimedSources      map[string]bool
	claimedDestinations map[string]bool
}

func (s *streamAccess) open(id string) (io.ReadCloser, error) {
	if source, ok := claimOnce(s, id, (*streamAccess).sourceHandles); ok {
		return source, nil
	}
	return nil, errors.New("stream source is unavailable")
}

func (s *streamAccess) create(id string) (io.WriteCloser, error) {
	if destination, ok := claimOnce(s, id, (*streamAccess).destinationHandles); ok {
		return destination, nil
	}
	return nil, errors.New("stream destination is unavailable")
}

func (s *streamAccess) sourceHandles() (map[string]io.ReadCloser, map[string]bool) {
	return s.sources, s.claimedSources
}

func (s *streamAccess) destinationHandles() (map[string]io.WriteCloser, map[string]bool) {
	return s.destinations, s.claimedDestinations
}

// claimOnce returns the stream attached as id and marks it claimed. It holds
// s's lock while handles selects the attached and claimed maps, and reports
// false when id is not attached or was already claimed.
func claimOnce[T any](s *streamAccess, id string, handles func(*streamAccess) (map[string]T, map[string]bool)) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	streams, claimed := handles(s)
	stream, ok := streams[id]
	if !ok || claimed[id] {
		var none T
		return none, false
	}
	claimed[id] = true
	return stream, true
}
