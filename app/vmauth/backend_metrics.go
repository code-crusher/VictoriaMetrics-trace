package main

import (
	"fmt"
	"sync/atomic"

	"github.com/VictoriaMetrics/metrics"
)

// backendMetrics holds per-backend metrics registered in AuthConfig.ms.
//
// A single backendMetrics instance is shared by all the backendURL objects with the same host,
// since vmauth cannot register multiple metrics with identical names.
//
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8292
type backendMetrics struct {
	// broken is the number of backendURL objects sharing bm, which are currently marked as broken.
	// The backend is considered as unavailable while this number is greater than zero.
	broken atomic.Int32

	// requests counts the requests sent to the backend.
	requests *metrics.Counter

	// errors counts the failed requests sent to the backend.
	errors *metrics.Counter
}

// isUp returns 1 if the backend is available and 0 otherwise.
func (bm *backendMetrics) isUp() float64 {
	if bm.broken.Load() == 0 {
		return 1
	}
	return 0
}

// initBackendMetrics registers per-backend metrics in ac.ms for all the backends
// defined in ac and assigns them to the corresponding backendURL objects.
//
// It must be called after urls of all the users from ac are initialized,
// i.e. after UserInfo.initURLs() is called for all the users.
//
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8292
func (ac *AuthConfig) initBackendMetrics() {
	getOrCreateBM := newBackendMetricsRegistry(ac.ms)

	uis := make([]*UserInfo, 0, len(ac.Users)+1)
	for i := range ac.Users {
		uis = append(uis, &ac.Users[i])
	}
	if ac.UnauthorizedUser != nil {
		uis = append(uis, ac.UnauthorizedUser)
	}
	for _, ui := range uis {
		registerBackendMetrics(getOrCreateBM, ui.URLPrefix)
		registerBackendMetrics(getOrCreateBM, ui.DefaultURL)
		for i := range ui.URLMaps {
			registerBackendMetrics(getOrCreateBM, ui.URLMaps[i].URLPrefix)
		}
	}
}

// registerBackendMetrics assigns per-backend metrics from getOrCreateBM
// to all the backends from up.
//
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8292
func registerBackendMetrics(getOrCreateBM func(host string) *backendMetrics, up *URLPrefix) {
	if up == nil {
		return
	}
	bus := up.bus.Load()
	up.bmByHost = make(map[string]*backendMetrics, len(bus.bus))
	for _, bu := range bus.bus {
		bm := getOrCreateBM(bu.url.Host)
		bu.bm = bm
		up.bmByHost[bu.url.Host] = bm
	}
}

// newBackendMetricsRegistry returns a function, which creates per-backend metrics
// in ms for the given backend host. The returned function caches the created metrics,
// so all the calls with the same host share the same metrics.
//
// See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/8292
func newBackendMetricsRegistry(ms *metrics.Set) func(host string) *backendMetrics {
	bmByHost := make(map[string]*backendMetrics)
	return func(host string) *backendMetrics {
		if bm := bmByHost[host]; bm != nil {
			return bm
		}
		labels := fmt.Sprintf("{backend=%q}", host)
		bm := &backendMetrics{}
		bm.requests = ms.GetOrCreateCounter(`vmauth_backend_requests_total` + labels)
		bm.errors = ms.GetOrCreateCounter(`vmauth_backend_errors_total` + labels)
		_ = ms.GetOrCreateGauge(`vmauth_backend_up`+labels, bm.isUp)
		bmByHost[host] = bm
		return bm
	}
}
