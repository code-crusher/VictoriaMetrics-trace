package promrelabel

import (
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promutil"
)

func TestGetScrapeURL(t *testing.T) {
	f := func(labelsStr, expectedScrapeURL, expectedAddress string) {
		t.Helper()
		labels := promutil.MustNewLabelsFromString(labelsStr)
		scrapeURL, address := GetScrapeURL(labels, nil)
		if scrapeURL != expectedScrapeURL {
			t.Fatalf("unexpected scrapeURL; got %q; want %q", scrapeURL, expectedScrapeURL)
		}
		if address != expectedAddress {
			t.Fatalf("unexpected address; got %q; want %q", address, expectedAddress)
		}
	}

	// Missing __address__
	f("{}", "", "")
	f(`{foo="bar"}`, "", "")

	// __address__ without port
	f(`{__address__="foo"}`, "http://foo/metrics", "foo:80")

	// __address__ with explicit port
	f(`{__address__="foo:1234"}`, "http://foo:1234/metrics", "foo:1234")

	// explicit __scheme__
	f(`{__address__="foo",__scheme__="https"}`, "https://foo/metrics", "foo:443")
	f(`{__address__="foo:1234",__scheme__="https"}`, "https://foo:1234/metrics", "foo:1234")

	// explicit __metrics_path__
	f(`{__address__="foo",__metrics_path__="abc"}`, "http://foo/abc", "foo:80")
	f(`{__address__="foo",__metrics_path__="/abc"}`, "http://foo/abc", "foo:80")
	f(`{__address__="foo",__metrics_path__="/ab/c?d=ef&aa=bb"}`, "http://foo/ab/c?d=ef&aa=bb", "foo:80")

	// explicit __param_*
	f(`{__address__="foo",__param_x="y"}`, "http://foo/metrics?x=y", "foo:80")
	f(`{__address__="foo",__param_x="y",__param_y="aa"}`, "http://foo/metrics?x=y&y=aa", "foo:80")
	f(`{__address__="foo",__param_x="y",__metrics_path__="?abc=de"}`, "http://foo/?abc=de&x=y", "foo:80")
	f(`{__address__="foo",__param_abc="y",__metrics_path__="?abc=de"}`, "http://foo/?abc=de&abc=y", "foo:80")

	// __address__ with metrics path and/or scheme
	f(`{__address__="foo/bar/baz?abc=de"}`, "http://foo/bar/baz?abc=de", "foo:80")
	f(`{__address__="foo:784/bar/baz?abc=de"}`, "http://foo:784/bar/baz?abc=de", "foo:784")
	f(`{__address__="foo:784/bar/baz?abc=de",__param_xx="yy"}`, "http://foo:784/bar/baz?abc=de&xx=yy", "foo:784")
	f(`{__address__="foo:784/bar/baz?abc=de",__param_xx="yy",__scheme__="https"}`, "https://foo:784/bar/baz?abc=de&xx=yy", "foo:784")
	f(`{__address__="http://foo/bar/baz?abc=de",__param_xx="yy"}`, "http://foo/bar/baz?abc=de&xx=yy", "foo:80")
	f(`{__address__="https://foo/bar/baz?abc=de",__param_xx="yy"}`, "https://foo/bar/baz?abc=de&xx=yy", "foo:443")

	// __address__ already carry 80/443 port
	f(`{__address__="foo:80"}`, "http://foo:80/metrics", "foo:80")
	f(`{__address__="foo:443"}`, "http://foo:443/metrics", "foo:443")
	f(`{__address__="http://foo"}`, "http://foo/metrics", "foo:80")
	f(`{__address__="https://foo"}`, "https://foo/metrics", "foo:443")
	f(`{__address__="http://foo:80"}`, "http://foo:80/metrics", "foo:80")
	f(`{__address__="https://foo:443"}`, "https://foo:443/metrics", "foo:443")
}

func TestExtractUserinfo(t *testing.T) {
	f := func(address, addressExpected, usernameExpected, passwordExpected string) {
		t.Helper()
		addressResult, username, password := ExtractUserinfo(address)
		if addressResult != addressExpected {
			t.Fatalf("unexpected address for %q; got %q; want %q", address, addressResult, addressExpected)
		}
		if username != usernameExpected {
			t.Fatalf("unexpected username for %q; got %q; want %q", address, username, usernameExpected)
		}
		if password != passwordExpected {
			t.Fatalf("unexpected password for %q; got %q; want %q", address, password, passwordExpected)
		}
	}

	// Missing userinfo
	f("", "", "", "")
	f("foo", "foo", "", "")
	f("foo:1234", "foo:1234", "", "")
	f("http://foo:1234/metrics?x=y", "http://foo:1234/metrics?x=y", "", "")
	f("[::1]:1234", "[::1]:1234", "", "")

	// userinfo without password
	f("user@foo", "foo", "user", "")
	f("user@foo:1234", "foo:1234", "user", "")
	f("http://user@foo/metrics", "http://foo/metrics", "user", "")

	// userinfo with password
	f("user:pass@foo", "foo", "user", "pass")
	f("user:pass@foo:1234", "foo:1234", "user", "pass")
	f("user:pass@foo:1234/metrics?x=y", "foo:1234/metrics?x=y", "user", "pass")
	f("http://user:pass@foo/metrics", "http://foo/metrics", "user", "pass")
	f("https://user:pass@foo:1234/metrics", "https://foo:1234/metrics", "user", "pass")

	// empty username and/or password
	f(":pass@foo", "foo", "", "pass")
	f("user:@foo", "foo", "user", "")
	f("@foo", "@foo", "", "")

	// escaped userinfo
	f("user:p%40ss@foo", "foo", "user", "p@ss")
	f("user%3Aname:pass@foo", "foo", "user:name", "pass")

	// `@` in the path or in query args mustn't be treated as the userinfo separator
	f("foo/metrics@bar", "foo/metrics@bar", "", "")
	f("foo/metrics?x=a@b", "foo/metrics?x=a@b", "", "")
}
