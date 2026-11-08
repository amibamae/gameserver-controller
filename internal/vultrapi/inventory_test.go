package vultrapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestCompleteInventoryPaginatesWithoutFilters(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/v2/instances" || r.URL.Query().Get("label") != "" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("unexpected inventory request: %s %s", r.Method, r.URL)
		}
		if calls == 1 {
			fmt.Fprint(w, `{"instances":[{"id":"example-a","os":"Example Linux"}],"meta":{"total":2,"links":{"next":"opaque cursor/&"}}}`)
		} else {
			if r.URL.Query().Get("cursor") != "opaque cursor/&" {
				t.Error("opaque cursor changed")
			}
			fmt.Fprint(w, `{"instances":[{"id":"example-b"}],"meta":{"total":2,"links":{"next":""}}}`)
		}
	})
	instances, err := client.ListInstances(context.Background(), []byte(testToken))
	if err != nil || len(instances) != 2 || calls != 2 || instances[0].OS != "Example Linux" {
		t.Fatalf("inventory=%#v calls=%d error=%v", instances, calls, err)
	}
}

func TestIncompleteInventoryNeverReturnsPartialSuccess(t *testing.T) {
	for name, pages := range map[string][]string{
		"missing next":           {`{"instances":[],"meta":{"total":0,"links":{}}}`},
		"missing total":          {`{"instances":[],"meta":{"links":{"next":""}}}`},
		"missing array":          {`{"meta":{"total":0,"links":{"next":""}}}`},
		"null array":             {`{"instances":null,"meta":{"total":0,"links":{"next":""}}}`},
		"missing links":          {`{"instances":[],"meta":{"total":0}}`},
		"truncated":              {`{"instances":[{"id":"a"}],"meta":{"total":2,"links":{"next":""}}}`},
		"duplicate":              {`{"instances":[{"id":"a"},{"id":"a"}],"meta":{"total":2,"links":{"next":""}}}`},
		"missing id":             {`{"instances":[{}],"meta":{"total":1,"links":{"next":""}}}`},
		"changing total":         {`{"instances":[{"id":"a"}],"meta":{"total":2,"links":{"next":"next"}}}`, `{"instances":[{"id":"b"}],"meta":{"total":3,"links":{"next":""}}}`},
		"duplicate across pages": {`{"instances":[{"id":"a"}],"meta":{"total":2,"links":{"next":"next"}}}`, `{"instances":[{"id":"a"}],"meta":{"total":2,"links":{"next":""}}}`},
		"cursor loop":            {`{"instances":[{"id":"a"}],"meta":{"total":3,"links":{"next":"next"}}}`, `{"instances":[{"id":"b"}],"meta":{"total":3,"links":{"next":"next"}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls >= len(pages) {
					t.Fatal("unexpected extra inventory request")
				}
				fmt.Fprint(w, pages[calls])
				calls++
			})
			instances, err := client.ListInstances(context.Background(), []byte(testToken))
			if !errors.Is(err, ErrInvalidResponse) || instances != nil {
				t.Fatalf("partial success=%#v error=%v", instances, err)
			}
		})
	}
}
