package adapter

import (
	"bytes"
	"io"
	"testing"
)

// These characterization tests pin the one-use stream handles a trusted
// controller attaches to a request context.

type adapterR5Closer struct{ bytes.Buffer }

func (*adapterR5Closer) Close() error { return nil }

func TestAdapterR5StreamHandles(t *testing.T) {
	source := io.NopCloser(bytes.NewReader(nil))
	var empty RequestContext
	if _, err := empty.OpenStream("s"); err == nil || err.Error() != "stream source is unavailable" {
		t.Fatalf("OpenStream without access = %v", err)
	}
	if _, err := empty.CreateStream("d"); err == nil || err.Error() != "stream destination is unavailable" {
		t.Fatalf("CreateStream without access = %v", err)
	}
	if empty.HasStreamDestination("d") {
		t.Fatal("destination reported without access")
	}
	var c RequestContext
	c.SetStreamSources(map[string]io.ReadCloser{"s": source, "": source})
	c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}, "": &adapterR5Closer{}})
	adapterR5ClaimTwice(t, "source", func(id string) error { _, err := c.OpenStream(id); return err }, "s", "stream source is unavailable")
	if !c.HasStreamDestination("d") || c.HasStreamDestination("") || c.HasStreamDestination("missing") {
		t.Fatal("HasStreamDestination misreported an attached handle")
	}
	adapterR5ClaimTwice(t, "destination", func(id string) error { _, err := c.CreateStream(id); return err }, "d", "stream destination is unavailable")
	if c.HasStreamDestination("d") {
		t.Fatal("claimed destination still reported")
	}
}

// adapterR5ClaimTwice requires claim to reject an empty and an unknown ID,
// accept id once, and then reject it with message.
func adapterR5ClaimTwice(t *testing.T, kind string, claim func(string) error, id, message string) {
	t.Helper()
	for _, rejected := range []string{"", "missing"} {
		if err := claim(rejected); err == nil || err.Error() != message {
			t.Fatalf("%s %q claim = %v, want %q", kind, rejected, err, message)
		}
	}
	if err := claim(id); err != nil {
		t.Fatalf("first %s claim = %v", kind, err)
	}
	if err := claim(id); err == nil || err.Error() != message {
		t.Fatalf("second %s claim = %v, want %q", kind, err, message)
	}
}

func TestAdapterR5StreamHandleAttachment(t *testing.T) {
	source := io.NopCloser(bytes.NewReader(nil))
	t.Run("destinations alone leave no sources", func(t *testing.T) {
		var c RequestContext
		c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}})
		if _, err := c.OpenStream("d"); err == nil || err.Error() != "stream source is unavailable" {
			t.Fatalf("OpenStream = %v", err)
		}
	})
	t.Run("sources alone leave no destinations", func(t *testing.T) {
		var c RequestContext
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		if _, err := c.CreateStream("s"); err == nil || err.Error() != "stream destination is unavailable" {
			t.Fatalf("CreateStream = %v", err)
		}
	})
	t.Run("setting sources replaces earlier destinations", func(t *testing.T) {
		var c RequestContext
		c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}})
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		if c.HasStreamDestination("d") {
			t.Fatal("destination survived SetStreamSources")
		}
	})
	t.Run("clearing removes every handle", func(t *testing.T) {
		var c RequestContext
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		c.ClearStreamSources()
		if _, err := c.OpenStream("s"); err == nil {
			t.Fatal("cleared source still open")
		}
	})
	t.Run("nil contexts are ignored", func(t *testing.T) {
		var c *RequestContext
		c.SetStreamSources(nil)
		c.SetStreamDestinations(nil)
		c.ClearStreamSources()
	})
}
