package promscrape

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/chunkedbuffer"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/httputil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promauth"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/proxy"
)

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

type connReadWriteCloser struct {
	io.Reader
	io.WriteCloser
}

func proxyTunnel(w http.ResponseWriter, r *http.Request) {
	transfer := func(src io.ReadCloser, dst io.WriteCloser) {
		defer dst.Close()
		defer src.Close()
		io.Copy(dst, src) //nolint
	}
	server, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	// For hijacked connections, one has to read from the connection buffer, but
	// still write directly to the connection.
	client := &connReadWriteCloser{clientBuf, clientConn}

	go transfer(client, server)
	transfer(server, client)
}

type testProxyServer struct {
	ba                   *promauth.BasicAuthConfig
	receivedProxyRequest bool
}

func checkBasicAuthHeader(w http.ResponseWriter, headerValue string, ba *promauth.BasicAuthConfig) bool {
	userPasswordEncoded := base64.StdEncoding.EncodeToString([]byte(ba.Username + ":" + ba.Password.String()))
	expectedAuthValue := "Basic " + userPasswordEncoded
	if headerValue != expectedAuthValue {
		w.WriteHeader(403)
		fmt.Fprintf(w, "Proxy Requires authorization got header value=%q, want=%q", headerValue, expectedAuthValue)
		return false
	}
	return true
}

func (tps *testProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tps.receivedProxyRequest = true
	if tps.ba != nil {
		if !checkBasicAuthHeader(w, r.Header.Get("Proxy-Authorization"), tps.ba) {
			return
		}
	}
	if r.Method == http.MethodConnect {
		proxyTunnel(w, r)
		return
	}

	tr := httputil.NewTransport(false, "test_client")
	resp, err := tr.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint
}

func newClientTestServer(useTLS bool, rh http.Handler) *httptest.Server {
	var s *httptest.Server
	if useTLS {
		s = httptest.NewTLSServer(rh)
	} else {
		s = httptest.NewServer(rh)
	}
	return s
}

func newTestAuthConfig(t *testing.T, isTLS bool, ba *promauth.BasicAuthConfig) *promauth.Config {
	a := promauth.Options{
		BasicAuth: ba,
	}
	if isTLS {
		a.TLSConfig = &promauth.TLSConfig{InsecureSkipVerify: true}
	}
	ac, err := a.NewConfig()
	if err != nil {
		t.Fatalf("cannot setup promauth.Config: %s", err)
	}
	return ac
}

func TestClientPerTargetCredentials(t *testing.T) {
	ctx := context.Background()

	// The scrape target at `user:pass@host:port` must receive the credentials from the address.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ahExpected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
		if ah := r.Header.Get("Authorization"); ah != ahExpected {
			w.WriteHeader(403)
			fmt.Fprintf(w, "unexpected Authorization header; got %q; want %q", ah, ahExpected)
			return
		}
		w.Write([]byte("metric_name{key=\"value\"} 123\n"))
	}))
	defer backend.Close()

	sws, err := getStaticScrapeWork([]byte(fmt.Sprintf(`
scrape_configs:
- job_name: foo
  static_configs:
  - targets: ["user:pass@%s"]
`, strings.TrimPrefix(backend.URL, "http://"))), "non-existing-file")
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if len(sws) != 1 {
		t.Fatalf("unexpected number of ScrapeWork items; got %d; want 1", len(sws))
	}
	sw := sws[0]
	if strings.Contains(sw.ScrapeURL, "pass") {
		t.Fatalf("credentials mustn't be exposed in the scrape URL; got %q", sw.ScrapeURL)
	}
	if strings.Contains(sw.Labels.String(), "pass") {
		t.Fatalf("credentials mustn't be exposed in target labels; got %s", sw.Labels.String())
	}
	sw.DisableKeepAlive = true

	c, err := newClient(ctx, sw)
	if err != nil {
		t.Fatalf("failed to create client: %s", err)
	}
	var cb chunkedbuffer.Buffer
	if _, err := c.ReadData(&cb); err != nil {
		t.Fatalf("unexpected error at ReadData: %s", err)
	}
}

func TestClientProxyReadOk(t *testing.T) {
	ctx := context.Background()
	f := func(isBackendTLS, isProxyTLS bool, backendAuth, proxyAuth *promauth.BasicAuthConfig) {
		t.Helper()

		proxyHandler := &testProxyServer{ba: proxyAuth}
		ps := newClientTestServer(isProxyTLS, proxyHandler)

		expectedBackendResponse := `metric_name{key="value"} 123\n`

		backend := newClientTestServer(isBackendTLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if backendAuth != nil && !checkBasicAuthHeader(w, r.Header.Get("Authorization"), backendAuth) {
				return
			}
			w.Write([]byte(expectedBackendResponse))
		}))

		defer backend.Close()
		defer ps.Close()

		c, err := newClient(ctx, &ScrapeWork{
			ScrapeURL: backend.URL,
			ProxyURL:  proxy.MustNewURL(ps.URL),
			// bump timeout for slow CIs
			ScrapeTimeout: 5 * time.Second,
			// force connection re-creating to avoid broken conns in slow CIs
			DisableKeepAlive:   true,
			AuthConfig:         newTestAuthConfig(t, isBackendTLS, backendAuth),
			ProxyAuthConfig:    newTestAuthConfig(t, isProxyTLS, proxyAuth),
			MaxScrapeSize:      16000,
			DisableCompression: true,
		})
		if err != nil {
			t.Fatalf("failed to create client: %s", err)
		}

		var cb chunkedbuffer.Buffer
		isGzipped, err := c.ReadData(&cb)
		if err != nil {
			t.Fatalf("unexpected error at ReadData: %s", err)
		}
		if isGzipped {
			t.Fatalf("the response mustn't be gzipped")
		}
		got, err := io.ReadAll(cb.NewReader())
		if err != nil {
			t.Fatalf("err read: %s", err)
		}

		if !proxyHandler.receivedProxyRequest {
			t.Fatalf("proxy server didn't received request")
		}
		if string(got) != expectedBackendResponse {
			t.Fatalf("not expected response: ")
		}
	}

	// no tls
	f(false, false, nil, nil)
	// both tls no auth
	f(true, true, nil, nil)
	// backend tls, proxy http no auth
	f(true, false, nil, nil)
	// backend http, proxy tls no auth
	f(false, true, nil, nil)

	// no tls with auth
	f(false, false, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, &promauth.BasicAuthConfig{Username: "proxy-test"})
	// proxy tls and auth
	f(false, true, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, &promauth.BasicAuthConfig{Username: "proxy-test"})
	// backend tls and auth
	f(true, false, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, &promauth.BasicAuthConfig{Username: "proxy-test"})
	// tls with auth
	f(true, true, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, &promauth.BasicAuthConfig{Username: "proxy-test"})

	// tls with backend auth
	f(true, true, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, nil)
	// tls with proxy auth
	f(true, true, nil, &promauth.BasicAuthConfig{Username: "proxy-test", Password: promauth.NewSecret("1234")})
	// proxy tls with backend auth
	f(false, true, &promauth.BasicAuthConfig{Username: "test", Password: promauth.NewSecret("1234")}, nil)
	// backend tls and proxy auth
	f(true, false, nil, &promauth.BasicAuthConfig{Username: "proxy-test", Password: promauth.NewSecret("1234")})
}
