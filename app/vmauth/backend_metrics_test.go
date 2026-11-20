package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metrics"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/netutil"
)

// getBackendMetricValue returns the value of the metric with the given name
// exposed by ms. It returns an empty string if the metric isn't exposed.
func getBackendMetricValue(ms *metrics.Set, name string) string {
	var bb strings.Builder
	ms.WritePrometheus(&bb)
	lines := strings.Split(bb.String(), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		return strings.TrimSpace(line[len(name):])
	}
	return ""
}

// tsHost returns the host:port part of the ts URL,
// which is used as the value of the `backend` label in tests.
func tsHost(ts *httptest.Server) string {
	u, err := url.Parse(ts.URL)
	if err != nil {
		panic(fmt.Errorf("BUG: cannot parse %q: %w", ts.URL, err))
	}
	return u.Host
}

func TestBackendMetrics(t *testing.T) {
	f := func(cfgStr string, expectedMetrics []string) {
		t.Helper()

		defer setAuthConfig(t, cfgStr)()
		ac := authConfig.Load()

		metricNames := ac.ms.ListMetricNames()
		for _, em := range expectedMetrics {
			found := false
			for _, mn := range metricNames {
				if mn == em {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing metric %q in the list of exposed metrics: %v", em, metricNames)
			}
		}
	}

	// per-backend metrics for unauthorized_user
	f(`
unauthorized_user:
  url_prefix: ["http://backend1:1234", "https://backend2/foo"]
`, []string{
		`vmauth_backend_up{backend="backend1:1234"}`,
		`vmauth_backend_up{backend="backend2"}`,
		`vmauth_backend_requests_total{backend="backend1:1234"}`,
		`vmauth_backend_requests_total{backend="backend2"}`,
		`vmauth_backend_errors_total{backend="backend1:1234"}`,
		`vmauth_backend_errors_total{backend="backend2"}`,
	})

	// per-backend metrics for a regular user, for default_url and for url_map
	f(`
users:
- username: "foo"
  url_prefix: "http://backend1"
  default_url: "http://backend2:2345"
  url_map:
  - src_paths: ["/foo"]
    url_prefix: "http://backend3:3456"
`, []string{
		`vmauth_backend_up{backend="backend1"}`,
		`vmauth_backend_up{backend="backend2:2345"}`,
		`vmauth_backend_up{backend="backend3:3456"}`,
		`vmauth_backend_requests_total{backend="backend1"}`,
		`vmauth_backend_requests_total{backend="backend2:2345"}`,
		`vmauth_backend_requests_total{backend="backend3:3456"}`,
		`vmauth_backend_errors_total{backend="backend1"}`,
		`vmauth_backend_errors_total{backend="backend2:2345"}`,
		`vmauth_backend_errors_total{backend="backend3:3456"}`,
	})

	// the same backend used by several users must be exposed only once
	f(`
users:
- username: "foo"
  url_prefix: "http://shared:1234"
- username: "bar"
  url_prefix: "http://shared:1234"
  url_map:
  - src_paths: ["/foo"]
    url_prefix: "http://shared:1234"
`, []string{
		`vmauth_backend_up{backend="shared:1234"}`,
		`vmauth_backend_requests_total{backend="shared:1234"}`,
		`vmauth_backend_errors_total{backend="shared:1234"}`,
	})
}

func TestBackendMetricsCounters(t *testing.T) {
	// Build backends directly, without loading the global auth config,
	// since this test manipulates the backend health checks.
	// The backend at ts.URL accepts TCP connections, so its health check
	// marks it as healthy again after the -failTimeout delay.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	}))
	defer ts.Close()

	ms := metrics.NewSet()
	up := mustParseURLs([]string{ts.URL, "http://backend2:1234"})
	registerBackendMetrics(newBackendMetricsRegistry(ms), up)

	bus := up.bus.Load()
	if len(bus.bus) != 2 {
		t.Fatalf("unexpected number of backends: %d; want 2", len(bus.bus))
	}

	// Make sure that all the backends are up initially.
	for _, bu := range bus.bus {
		if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_up{backend=%q}", bu.url.Host)); v != "1" {
			t.Fatalf("unexpected vmauth_backend_up value for %q: %q; want 1", bu.url.Host, v)
		}
	}

	// Break the first backend and make sure that vmauth_backend_up is set to zero for it.
	bu0 := bus.bus[0]
	if bu0.url.Host != tsHost(ts) {
		t.Fatalf("unexpected first backend host: %q; want %q", bu0.url.Host, tsHost(ts))
	}
	bu0.setBroken()
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_up{backend=%q}", bu0.url.Host)); v != "0" {
		t.Fatalf("unexpected vmauth_backend_up value for the broken backend %q: %q; want 0", bu0.url.Host, v)
	}

	// The second backend must remain up.
	bu1 := bus.bus[1]
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_up{backend=%q}", bu1.url.Host)); v != "1" {
		t.Fatalf("unexpected vmauth_backend_up value for %q: %q; want 1", bu1.url.Host, v)
	}

	// Wait until the health check marks the broken backend as healthy again.
	// The default -failTimeout is used, so the health check may take up to
	// -failTimeout plus the dial timeout to complete.
	bu0.bhc.wg.Wait()
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_up{backend=%q}", bu0.url.Host)); v != "1" {
		t.Fatalf("unexpected vmauth_backend_up value for the recovered backend %q: %q; want 1", bu0.url.Host, v)
	}

	// Make sure that the per-backend request and error counters are incremented.
	bu0.incBackendRequests()
	bu0.incBackendRequests()
	bu0.incBackendErrors()
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_requests_total{backend=%q}", bu0.url.Host)); v != "2" {
		t.Fatalf("unexpected vmauth_backend_requests_total value for %q: %q; want 2", bu0.url.Host, v)
	}
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_errors_total{backend=%q}", bu1.url.Host)); v != "0" {
		t.Fatalf("unexpected vmauth_backend_errors_total value for %q: %q; want 0", bu1.url.Host, v)
	}
	if v := getBackendMetricValue(ms, fmt.Sprintf("vmauth_backend_errors_total{backend=%q}", bu0.url.Host)); v != "1" {
		t.Fatalf("unexpected vmauth_backend_errors_total value for %q: %q; want 1", bu0.url.Host, v)
	}

	// Stop the health checks of the backends, so they don't outlive the test
	// and don't interfere with the other tests.
	bus.stopHealthChecks()
}

func TestBackendMetricsRequestHandling(t *testing.T) {
	// Send a request through vmauth and make sure that the request
	// is counted at the per-backend metric of the backend it was sent to.
	f := func(cfgStr, requestURL string, backendHandler http.HandlerFunc, expectedMetrics string) {
		t.Helper()

		ts := httptest.NewServer(backendHandler)
		defer ts.Close()

		cfgStr = strings.ReplaceAll(cfgStr, "{BACKEND}", ts.URL)
		expectedMetrics = strings.ReplaceAll(expectedMetrics, "{BACKEND_HOST}", tsHost(ts))

		defer setAuthConfig(t, cfgStr)()

		r, err := http.NewRequest(http.MethodGet, requestURL, nil)
		if err != nil {
			t.Fatalf("cannot initialize http request: %s", err)
		}
		r.RequestURI = r.URL.RequestURI()
		r.RemoteAddr = "42.2.3.84:6789"

		w := &fakeResponseWriter{}
		if !requestHandlerWithInternalRoutes(w, r) {
			t.Fatalf("unexpected false returned from requestHandler")
		}

		ac := authConfig.Load()
		expectedMetrics = strings.TrimSpace(expectedMetrics)
		for _, em := range strings.Split(expectedMetrics, "\n") {
			name, value, ok := strings.Cut(em, " ")
			if !ok {
				t.Fatalf("missing value in the expected metric line %q", em)
			}
			if v := getBackendMetricValue(ac.ms, name); v != value {
				t.Fatalf("unexpected value for %q: %q; want %q", name, v, value)
			}
		}
	}

	// A successful request must be counted at vmauth_backend_requests_total
	// of the backend it was sent to.
	f(`
unauthorized_user:
  url_prefix: {BACKEND}/foo`, "http://some-host.com/abc",
		func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "response")
		}, `
vmauth_backend_requests_total{backend="{BACKEND_HOST}"} 1
vmauth_backend_errors_total{backend="{BACKEND_HOST}"} 0
vmauth_backend_up{backend="{BACKEND_HOST}"} 1`)

	// A failed request must be counted at vmauth_backend_errors_total
	// of the backend it was sent to.
	f(`
unauthorized_user:
  url_prefix: {BACKEND}/foo`, "http://some-host.com/abc",
		func(w http.ResponseWriter, _ *http.Request) {
			// Close the connection without sending a valid http response.
			// This makes vmauth mark the backend as broken.
			panic(http.ErrAbortHandler)
		}, `
vmauth_backend_requests_total{backend="{BACKEND_HOST}"} 1
vmauth_backend_errors_total{backend="{BACKEND_HOST}"} 1
vmauth_backend_up{backend="{BACKEND_HOST}"} 0`)
}

func TestBackendMetricsConfigReload(t *testing.T) {
	// Per-backend metrics from the previous config must be removed
	// on config reload, while the new config must register its own metrics.
	defer setAuthConfig(t, `
users:
- username: "foo"
  url_prefix: "http://backend1:1234"`)()

	ac := authConfig.Load()
	if names := ac.ms.ListMetricNames(); !slices.Contains(names, `vmauth_backend_up{backend="backend1:1234"}`) {
		t.Fatalf("missing vmauth_backend_up{backend=\"backend1:1234\"} in %v", names)
	}

	defer setAuthConfig(t, `
users:
- username: "foo"
  url_prefix: "http://backend2:2345"`)()

	ac = authConfig.Load()
	names := ac.ms.ListMetricNames()
	if slices.Contains(names, `vmauth_backend_up{backend="backend1:1234"}`) {
		t.Fatalf("stale vmauth_backend_up{backend=\"backend1:1234\"} is exposed after config reload: %v", names)
	}
	if !slices.Contains(names, `vmauth_backend_up{backend="backend2:2345"}`) {
		t.Fatalf("missing vmauth_backend_up{backend=\"backend2:2345\"} in %v", names)
	}
}

func TestBackendMetricsDiscoverBackendIPs(t *testing.T) {
	// Per-backend metrics must be propagated to the backends
	// discovered via DNS.
	fr := newFakeResolver(nil,
		map[string][]net.IPAddr{
			"backend1.local": {
				{
					IP: net.ParseIP("10.0.10.13"),
				},
			},
		},
	)
	origResolver := netutil.Resolver
	defer func() {
		netutil.Resolver = origResolver
	}()
	netutil.Resolver = fr

	defer setAuthConfig(t, `
unauthorized_user:
  discover_backend_ips: true
  url_prefix: "http://backend1.local:1234"`)()

	ac := authConfig.Load()
	up := ac.UnauthorizedUser.URLPrefix

	// The metrics for the backend1.local host must be registered before the discovery.
	bmOrig, ok := up.bmByHost["backend1.local:1234"]
	if !ok {
		t.Fatalf("missing per-backend metrics for backend1.local:1234 in %v", up.bmByHost)
	}

	up.discoverBackendAddrsIfNeeded()

	bus := up.bus.Load()
	if len(bus.bus) != 1 {
		t.Fatalf("unexpected number of the discovered backends: %d; want 1", len(bus.bus))
	}
	bu := bus.bus[0]
	if bu.url.Host != "10.0.10.13:1234" {
		t.Fatalf("unexpected host of the discovered backend: %q; want %q", bu.url.Host, "10.0.10.13:1234")
	}
	if bu.bm != bmOrig {
		t.Fatalf("the discovered backend must share per-backend metrics with backend1.local")
	}

	// Make sure that the requests to the discovered backend
	// are counted at the metrics of the original host.
	bu.incBackendRequests()
	if v := getBackendMetricValue(ac.ms, `vmauth_backend_requests_total{backend="backend1.local:1234"}`); v != "1" {
		t.Fatalf("unexpected vmauth_backend_requests_total value: %q; want 1", v)
	}
	if v := getBackendMetricValue(ac.ms, `vmauth_backend_requests_total{backend="10.0.10.13:1234"}`); v != "" {
		t.Fatalf("unexpected metric for the discovered backend ip: %q; want it to be missing", v)
	}
}
