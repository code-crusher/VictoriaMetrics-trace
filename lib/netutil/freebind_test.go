package netutil

import (
	"flag"
	"fmt"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// freebindAddrs contains IPv4 addresses from the TEST-NET-1 block,
// which is reserved for documentation purposes and must never be assigned
// to network interfaces. See https://datatracker.ietf.org/doc/html/rfc5737
//
// Each test case uses a distinct address, since NewTCPListener registers
// metrics with the addr label and panics on duplicated metric names.
const (
	freebindIPv4Addr  = "192.0.2.10:12345"
	freebindIPv4Addr2 = "192.0.2.11:12346"
)

func setFreebindFlag(t *testing.T, enabled bool) {
	t.Helper()

	origValue := *freeBindAddress
	if err := flag.Set("freeBindAddress", strconv.FormatBool(enabled)); err != nil {
		t.Fatalf("cannot set -freeBindAddress=%t: %s", enabled, err)
	}
	t.Cleanup(func() {
		if err := flag.Set("freeBindAddress", strconv.FormatBool(origValue)); err != nil {
			t.Fatalf("cannot restore -freeBindAddress=%t: %s", origValue, err)
		}
	})
}

func TestFreebindTCPListenerDisabled(t *testing.T) {
	if !isLinuxOS() {
		t.Skipf("skipping test, since IP_FREEBIND socket option is Linux-only; current OS: %s", runtime.GOOS)
	}

	setFreebindFlag(t, false)

	// Listening on a non-existing IP address must fail
	// when the -freeBindAddress command-line flag isn't set.
	//
	// The listener name contains a random suffix, so the test can be run
	// multiple times with -count flag without panicking because of
	// duplicated metric names registered by NewTCPListener.
	ln, err := NewTCPListener(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), freebindIPv4Addr, false, nil)
	if err == nil {
		_ = ln.Close()
		t.Fatalf("expecting non-nil error when listening on non-existing addr %q without -freeBindAddress", freebindIPv4Addr)
	}
}

func TestFreebindTCPListenerEnabled(t *testing.T) {
	if !isLinuxOS() {
		t.Skipf("skipping test, since IP_FREEBIND socket option is Linux-only; current OS: %s", runtime.GOOS)
	}

	setFreebindFlag(t, true)

	// Listening on a non-existing IP address must succeed
	// when the -freeBindAddress command-line flag is set.
	//
	// The listener name contains a random suffix, so the test can be run
	// multiple times with -count flag without panicking because of
	// duplicated metric names registered by NewTCPListener.
	ln, err := NewTCPListener(fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano()), freebindIPv4Addr2, false, nil)
	if err != nil {
		t.Fatalf("unexpected error when listening on non-existing addr %q with -freeBindAddress: %s", freebindIPv4Addr2, err)
	}
	defer ln.Close()

	if ln.Addr().String() != freebindIPv4Addr2 {
		t.Fatalf("unexpected listener addr; got %q; want %q", ln.Addr().String(), freebindIPv4Addr2)
	}
}

func TestFreebindUDPListenPacket(t *testing.T) {
	if !isLinuxOS() {
		t.Skipf("skipping test, since IP_FREEBIND socket option is Linux-only; current OS: %s", runtime.GOOS)
	}

	setFreebindFlag(t, true)

	// Listening on a non-existing IP address must succeed
	// when the -freeBindAddress command-line flag is set.
	pc, err := NewUDPListenPacket("udp4", "192.0.2.12:12347")
	if err != nil {
		t.Fatalf("unexpected error when listening on non-existing addr with -freeBindAddress: %s", err)
	}
	_ = pc.Close()

	setFreebindFlag(t, false)

	// The same must fail when the -freeBindAddress command-line flag isn't set.
	pc, err = NewUDPListenPacket("udp4", "192.0.2.13:12348")
	if err == nil {
		_ = pc.Close()
		t.Fatalf("expecting non-nil error when listening on non-existing addr without -freeBindAddress")
	}
}

func isLinuxOS() bool {
	return runtime.GOOS == "linux"
}
