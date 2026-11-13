package promrelabel

import (
	"net/url"
	"strings"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/bytesutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

// GetScrapeURL makes scrape url and __address_ labels for the given labels and extraParams.
func GetScrapeURL(labels *promutil.Labels, extraParams map[string][]string) (string, string) {
	// See https://www.robustperception.io/life-of-a-label
	scheme := labels.Get("__scheme__")
	if len(scheme) == 0 {
		scheme = "http"
	}
	metricsPath := labels.Get("__metrics_path__")
	if len(metricsPath) == 0 {
		metricsPath = "/metrics"
	}
	address := labels.Get("__address__")
	if len(address) == 0 {
		return "", ""
	}
	// Usability extension to Prometheus behavior: extract optional scheme and metricsPath from __address__.
	// Prometheus silently drops targets with __address__ containing scheme or metricsPath
	// according to https://www.robustperception.io/life-of-a-label/ .
	if strings.HasPrefix(address, "http://") {
		scheme = "http"
		address = address[len("http://"):]
	} else if strings.HasPrefix(address, "https://") {
		scheme = "https"
		address = address[len("https://"):]
	}
	if n := strings.IndexByte(address, '/'); n >= 0 {
		metricsPath = address[n:]
		address = address[:n]
	}

	// If port is missing, typically it should be 80/443. This WAS written in a label and used as scrapeURL.
	// However, adding the port by default can cause some issues, see: https://github.com/prometheus/prometheus/pull/9523#issuecomment-2059314966
	// After https://github.com/VictoriaMetrics/VictoriaMetrics/issues/6792:
	// - don't add the default port to scrapeURL.
	// - continue adding the default port to the label value for backward compatibility and avoid generating new time series.
	addressMustWithPort := addMissingPort(address, scheme == "https")

	if !strings.HasPrefix(metricsPath, "/") {
		metricsPath = "/" + metricsPath
	}
	params := getParamsFromLabels(labels, extraParams)
	optionalQuestion := ""
	if len(params) > 0 {
		optionalQuestion = "?"
		if strings.Contains(metricsPath, "?") {
			optionalQuestion = "&"
		}
	}
	paramsStr := url.Values(params).Encode()
	scrapeURL := buildScrapeURL(scheme, address, metricsPath, optionalQuestion, paramsStr)
	return scrapeURL, addressMustWithPort
}

// ExtractUserinfo extracts optional basic auth credentials from the userinfo part of the given address
// in the form `[scheme://][username[:password]@]host[:port][/path][?query]`.
//
// It returns the address without the userinfo, plus the extracted username and password.
// The returned address is equal to the given address if the userinfo is missing.
//
// Credentials from the address must be extracted into the per-target basic auth config, since otherwise
// they are exposed in the `instance` label, at the `/targets` page and in scrape error messages.
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/9733
func ExtractUserinfo(address string) (string, string, string) {
	// Skip the optional scheme prefix, so the authority part could be located.
	prefix := ""
	rest := address
	if n := strings.Index(rest, "://"); n >= 0 {
		prefix = rest[:n+3]
		rest = rest[n+3:]
	}
	// Search for the userinfo only in the authority part of the address, since `@` may occur in the path or in query args.
	authority := rest
	if n := strings.IndexAny(authority, "/?#"); n >= 0 {
		authority = authority[:n]
	}
	n := strings.LastIndexByte(authority, '@')
	if n < 0 {
		return address, "", ""
	}
	userinfo := authority[:n]
	username := userinfo
	password := ""
	if i := strings.IndexByte(userinfo, ':'); i >= 0 {
		username = userinfo[:i]
		password = userinfo[i+1:]
	}
	if username == "" && password == "" {
		return address, "", ""
	}
	return prefix + rest[n+1:], unescapeUserinfo(username), unescapeUserinfo(password)
}

func unescapeUserinfo(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	d, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return d
}

func getParamsFromLabels(labels *promutil.Labels, extraParams map[string][]string) map[string][]string {
	// See https://www.robustperception.io/life-of-a-label
	var m map[string][]string
	for _, label := range labels.GetLabels() {
		if !strings.HasPrefix(label.Name, "__param_") {
			continue
		}
		name := label.Name[len("__param_"):]
		values := []string{label.Value}
		if p := extraParams[name]; len(p) > 1 {
			values = append(values, p[1:]...)
		}
		if m == nil {
			m = make(map[string][]string)
		}
		m[name] = values
	}
	return m
}

func buildScrapeURL(scheme, address, metricsPath, optionalQuestion, paramsStr string) string {
	bb := bbPool.Get()
	b := bb.B[:0]
	b = append(b, scheme...)
	b = append(b, "://"...)
	b = append(b, address...)
	b = append(b, metricsPath...)
	b = append(b, optionalQuestion...)
	b = append(b, paramsStr...)
	s := bytesutil.InternBytes(b)
	bb.B = b
	bbPool.Put(bb)
	return s
}

func addMissingPort(addr string, isTLS bool) string {
	if strings.Contains(addr, ":") {
		return addr
	}
	if isTLS {
		return concatTwoStrings(addr, ":443")
	}
	return concatTwoStrings(addr, ":80")
}

func concatTwoStrings(x, y string) string {
	bb := bbPool.Get()
	b := bb.B[:0]
	b = append(b, x...)
	b = append(b, y...)
	s := bytesutil.InternBytes(b)
	bb.B = b
	bbPool.Put(bb)
	return s
}

var bbPool bytesutil.ByteBufferPool
