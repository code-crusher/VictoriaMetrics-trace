package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This test reproduces https://github.com/VictoriaMetrics/VictoriaMetrics/issues/11594
// A vmuser with access to a single tenant (via AccountID/ProjectID headers)
// must not be able to override these headers with arbitrary values.
func TestTenantHeadersNotOverridableByClient(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s\nAccountID=%s\nProjectID=%s", r.URL.Path, r.Header.Get("AccountID"), r.Header.Get("ProjectID"))
	}))
	defer ts.Close()

	cfgStr := fmt.Sprintf(`
users:
  - username: "foo"
    password: "bar"
    url_map:
      - src_paths:
        - "/vlogs/select/.*"
        headers:
        - "AccountID: 1"
        - "ProjectID: 1"
        drop_src_path_prefix_parts: 1
        url_prefix: %s`, ts.URL)

	defer setAuthConfig(t, cfgStr)()

	r, err := http.NewRequest(http.MethodGet, "http://some-host.com/vlogs/select/1/2/logsql/query", nil)
	if err != nil {
		t.Fatalf("cannot initialize http request: %s", err)
	}
	r.RequestURI = r.URL.RequestURI()
	r.SetBasicAuth("foo", "bar")
	// Malicious client spoofs tenant headers
	r.Header.Set("AccountID", "42")
	r.Header.Set("ProjectID", "43")

	w := &fakeResponseWriter{}
	if !requestHandlerWithInternalRoutes(w, r) {
		t.Fatalf("unexpected false is returned from requestHandler")
	}

	response := w.getResponse()
	responseExpected := fmt.Sprintf("statusCode=200\npath=/select/1/2/logsql/query\nAccountID=1\nProjectID=1")
	if response != responseExpected {
		t.Fatalf("unexpected response\ngot\n%s\nwant\n%s", response, responseExpected)
	}
}
