package vultrapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestInstanceMainIP(t *testing.T) {
	cases := map[string]struct {
		mainIP string
		want   string
		err    error
	}{
		"assigned":     {mainIP: "203.0.113.20", want: "203.0.113.20"},
		"pending":      {mainIP: "0.0.0.0", err: ErrIPNotAssigned},
		"empty":        {mainIP: "", err: ErrIPNotAssigned},
		"private":      {mainIP: "10.0.0.5", err: ErrInvalidResponse},
		"not an IP":    {mainIP: "example.com", err: ErrInvalidResponse},
		"IPv6 instead": {mainIP: "2001:db8::1", err: ErrInvalidResponse},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"instance":{"id":"inst-123","main_ip":%q}}`, test.mainIP)
			})
			ip, err := client.InstanceMainIP(context.Background(), []byte(testToken), "inst-123")
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Fatalf("error = %v, want %v", err, test.err)
				}
				return
			}
			if err != nil || ip.String() != test.want {
				t.Fatalf("InstanceMainIP = %v, %v", ip, err)
			}
		})
	}
}
