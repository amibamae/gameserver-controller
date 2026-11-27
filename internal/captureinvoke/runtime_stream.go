package captureinvoke

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// captureStream is the private stage and the issued broker stream for one
// dispatched capture. counted measures the bytes written to the stage.
type captureStream struct {
	stage   Stage
	counted *countingSink
	ref     capturestream.Reference
	ticket  *capturestream.Ticket
	sealed  bool
}

// openCaptureStream opens the stage, claims the external destination when the
// request names one, and issues the broker stream that writes to both. Every
// failure after the stage opens discards it.
func (r *Runtime) openCaptureStream(ctx context.Context, c adapter.RequestContext, scope capturestream.Scope) (*captureStream, error) {
	stage, err := r.staging.Open(ctx, scope)
	if errors.Is(err, ErrStagingNeedsReconciliation) {
		return nil, uncertain()
	}
	if err != nil || stage == nil {
		return nil, errors.New("capture staging is unavailable")
	}
	mirror, err := claimCaptureDestination(c)
	if err != nil {
		return nil, discardBeforeDispatch(stage, "capture destination could not be claimed")
	}
	counted := &countingSink{sink: stage, mirror: mirror}
	ref, ticket, err := r.broker.IssueTracked(scope, r.maximum, captureStreamExpiry(c.Deadline), c.Deadline, counted)
	if err != nil {
		return nil, discardBeforeDispatch(stage, "capture stream could not be issued")
	}
	return &captureStream{stage: stage, counted: counted, ref: ref, ticket: ticket}, nil
}

// claimCaptureDestination claims the request's one external stream
// destination, if it names one; otherwise it returns no writer.
func claimCaptureDestination(c adapter.RequestContext) (io.Writer, error) {
	if len(c.StreamTransfers) != 1 {
		return nil, nil
	}
	return c.CreateStream(c.StreamTransfers[0].TransferID)
}

// captureStreamExpiry is five minutes from now, or the invocation deadline
// when that is sooner.
func captureStreamExpiry(deadline time.Time) time.Time {
	expiry := time.Now().Add(5 * time.Minute)
	if deadline.Before(expiry) {
		expiry = deadline
	}
	return expiry
}

// discardBeforeDispatch discards stage after a setup failure and returns the
// failure, or a cleanup error when the discard fails.
func discardBeforeDispatch(stage Stage, message string) error {
	if stage.Discard() != nil {
		return errors.New("capture staging cleanup failed before dispatch")
	}
	return errors.New(message)
}

// release aborts the stream ticket and discards the stage unless it was
// sealed. It reports false when the discard failed.
func (s *captureStream) release() bool {
	s.ticket.Abort()
	return s.sealed || s.stage.Discard() == nil
}

// countingSink writes to the stage and then, when the stage took every byte,
// to the optional external mirror. size counts the bytes the stage took.
type countingSink struct {
	sink   io.Writer
	mirror io.Writer
	size   atomic.Int64
}

func (w *countingSink) Write(p []byte) (int, error) {
	n, err := w.sink.Write(p)
	if w.mirrorsWrite(n, err, p) {
		if mirrored, mirrorErr := writeAll(w.mirror, p); mirrorErr != nil {
			return mirrored, mirrorErr
		}
	}
	if n > 0 && n <= len(p) {
		w.size.Add(int64(n))
	}
	return n, err
}

// mirrorsWrite reports whether a stage write of n bytes of p with err must
// also go to the mirror: the stage took all of p and a mirror is attached.
func (w *countingSink) mirrorsWrite(n int, err error, p []byte) bool {
	return err == nil && n == len(p) && w.mirror != nil
}

// writeAll writes p to w and reports io.ErrShortWrite when w took fewer
// bytes without an error.
func writeAll(w io.Writer, p []byte) (int, error) {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
