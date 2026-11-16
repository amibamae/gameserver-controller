package cloudflaredns

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

func constructorConfig() Config {
	return Config{
		AdapterID: "integration.cloudflare-dns", EnvironmentID: "test", ZoneID: testZoneID,
		ZoneName: "example.com", APITokenRef: tokenRef,
		Records: map[string]ManagedRecord{"PLAY.EXAMPLE.COM.": {
			Purposes: []Purpose{PurposeFallback},
			Fallback: map[string][]string{"A": {"192.0.2.2", "192.0.2.1"}},
		}},
	}
}

func TestConstructorChecksConfigBeforeAPIBase(t *testing.T) {
	config := constructorConfig()
	config.ZoneID = ""
	config.APITokenRef = ""
	_, err := newAdapter(config, "test", nil, nil, "http://example.invalid", time.Now)
	if err == nil || err.Error() != "a Cloudflare zone ID and zone name are required" {
		t.Fatalf("error = %v, want zone validation first", err)
	}

	config = constructorConfig()
	config.Records["PLAY.EXAMPLE.COM."] = ManagedRecord{Purposes: []Purpose{PurposeFallback}}
	_, err = newAdapter(config, "test", staticCredential("test"), nil, "http://example.invalid", time.Now)
	if err == nil || err.Error() != "fallback records need configured fallback values" {
		t.Fatalf("error = %v, want fallback validation before API base validation", err)
	}
}

func TestConstructorNormalizesRecordsAndCopiesHTTPClient(t *testing.T) {
	config := constructorConfig()
	caller := &http.Client{}
	dns, err := New(config, "test", staticCredential("test"), caller)
	if err != nil {
		t.Fatal(err)
	}
	if got := dns.records["play.example.com"].Fallback["A"]; !reflect.DeepEqual(got, []string{"192.0.2.1", "192.0.2.2"}) {
		t.Fatalf("fallback addresses = %v, want canonical sorted values", got)
	}
	if caller.Timeout != 0 || caller.CheckRedirect != nil {
		t.Fatal("constructor mutated the caller's HTTP client")
	}
	if dns.client.http.Timeout != defaultRequestTimeout || dns.client.http.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("adapter client did not apply the default timeout and redirect policy")
	}
}
