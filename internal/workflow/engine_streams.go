package workflow

import (
	"io"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// preparedStreams holds a step's validated stream transfers until the call
// ends; close releases every end once.
type preparedStreams struct {
	transfers    []adapter.StreamTransfer
	references   []capturestream.Reference
	sources      map[string]io.ReadCloser
	destinations map[string]io.WriteCloser
	closed       bool
}

// newPreparedStreams indexes transfers by ID. If any transfer has the wrong
// ends for its direction, no verifier, no ID, no byte bound, or a repeated
// ID, it closes every end it was given and reports false.
func newPreparedStreams(transfers []adapter.StreamTransfer) (*preparedStreams, bool) {
	streams := &preparedStreams{
		transfers:    transfers,
		references:   make([]capturestream.Reference, len(transfers)),
		sources:      make(map[string]io.ReadCloser, len(transfers)),
		destinations: make(map[string]io.WriteCloser, len(transfers)),
	}
	for index, transfer := range transfers {
		id := transfer.Reference.TransferID
		_, duplicate := streams.sources[id]
		if _, ok := streams.destinations[id]; ok {
			duplicate = true
		}
		if !wellFormedTransfer(transfer) || duplicate {
			closeAllStreamEnds(transfers)
			return nil, false
		}
		streams.references[index] = transfer.Reference
		if transfer.Source != nil {
			streams.sources[id] = transfer.Source
		} else {
			streams.destinations[id] = transfer.Sink
		}
	}
	return streams, true
}

// wellFormedTransfer reports whether a transfer has exactly the end its
// direction needs, a verifier, an ID and a positive byte bound.
func wellFormedTransfer(transfer adapter.StreamTransfer) bool {
	validBinding := false
	switch transfer.Reference.Direction {
	case "controller_to_adapter":
		validBinding = transfer.Source != nil && transfer.Sink == nil
	case "adapter_to_controller":
		validBinding = transfer.Sink != nil && transfer.Source == nil
	}
	return validBinding && transfer.Verify != nil && transfer.Reference.TransferID != "" && transfer.Reference.MaximumBytes >= 1
}

// closeAllStreamEnds releases every end of rejected transfers. The step is
// refused before any adapter call, so a close error has nothing to change.
func closeAllStreamEnds(transfers []adapter.StreamTransfer) {
	for _, prepared := range transfers {
		if prepared.Source != nil {
			_ = prepared.Source.Close()
		}
		if prepared.Sink != nil {
			_ = prepared.Sink.Close()
		}
	}
}

// close closes each transfer's end once and returns the first error.
func (p *preparedStreams) close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var closeErr error
	for _, transfer := range p.transfers {
		var err error
		if transfer.Source != nil {
			err = transfer.Source.Close()
		} else if transfer.Sink != nil {
			err = transfer.Sink.Close()
		}
		if err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

// verify returns the first transfer whose verifier rejects what passed.
func (p *preparedStreams) verify() error {
	for _, transfer := range p.transfers {
		if err := transfer.Verify(); err != nil {
			return err
		}
	}
	return nil
}
