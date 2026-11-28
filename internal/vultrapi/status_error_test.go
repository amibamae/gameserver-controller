package vultrapi

import (
	"errors"
	"net/http"
	"testing"
)

func TestStatusErrorCarriesTheHTTPStatusWithUnchangedMessages(t *testing.T) {
	for _, test := range []struct {
		status   int
		sentinel error
		message  string
	}{
		{http.StatusNotFound, ErrProviderReject, "Vultr rejected the request: HTTP 404"},
		{http.StatusTooManyRequests, ErrProviderReject, "Vultr rejected the request: HTTP 429"},
		{http.StatusServiceUnavailable, ErrRequestFailed, "Vultr API request failed: HTTP 503"},
	} {
		err := statusError(test.status, false)
		var status StatusError
		if !errors.Is(err, test.sentinel) || !errors.As(err, &status) || int(status) != test.status || err.Error() != test.message {
			t.Fatalf("status %d: err = %v (status %d)", test.status, err, status)
		}
	}
}
