package dashboards_test

// This test validates the official Grafana dashboards shipped in this directory.
//
// The thresholds on the resource usage panels are derived from the official
// alerting rules at deployment/docker/rules/ and from the capacity planning
// docs at https://docs.victoriametrics.com/victoriametrics/quick-start/#capacity-planning,
// so users can tell whether their deployment is healthy directly from the
// dashboard. See https://github.com/VictoriaMetrics/VictoriaMetrics/issues/7603.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type step struct {
	Color string   `json:"color"`
	Value *float64 `json:"value"`
}

type thresholds struct {
	Mode  string `json:"mode"`
	Steps []step `json:"steps"`
}

type thresholdsStyle struct {
	Mode string `json:"mode"`
}

type custom struct {
	ThresholdsStyle thresholdsStyle `json:"thresholdsStyle"`
}

type fieldConfigDefaults struct {
	Custom     *custom    `json:"custom"`
	Thresholds thresholds `json:"thresholds"`
	Unit       string     `json:"unit"`
}

type fieldConfig struct {
	Defaults fieldConfigDefaults `json:"defaults"`
}

type panel struct {
	ID          int         `json:"id"`
	Title       string      `json:"title"`
	FieldConfig fieldConfig `json:"fieldConfig"`
	Panels      []*panel    `json:"panels"`
}

type dashboard struct {
	Title  string   `json:"title"`
	UID    string   `json:"uid"`
	Panels []*panel `json:"panels"`
}

// findDashboards returns the Prometheus-datasource dashboards in this directory.
// Tests run with this directory as the working directory.
func findDashboards(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read dashboards directory: %s", err)
	}
	res := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		res[e.Name()] = e.Name()
	}
	if len(res) == 0 {
		t.Fatal("no dashboards found")
	}
	return res
}

func loadDashboard(t *testing.T, path string) *dashboard {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %s", path, err)
	}
	var d dashboard
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatalf("cannot parse %s: %s", path, err)
	}
	return &d
}

// allPanels appends all the panels with configured thresholds, including the
// panels nested inside row panels.
func allPanels(panels []*panel, out *[]*panel) {
	for _, p := range panels {
		if len(p.FieldConfig.Defaults.Thresholds.Steps) > 0 {
			*out = append(*out, p)
		}
		allPanels(p.Panels, out)
	}
}

// stepsValues returns the threshold values of the non-base steps.
func stepsValues(t *testing.T, p *panel) []float64 {
	t.Helper()
	steps := p.FieldConfig.Defaults.Thresholds.Steps
	if len(steps) == 0 {
		t.Fatalf("panel %d (%q) has no thresholds", p.ID, p.Title)
	}
	var vals []float64
	for _, s := range steps[1:] {
		if s.Value == nil {
			t.Fatalf("panel %d (%q): threshold step with color %q has no value", p.ID, p.Title, s.Color)
		}
		vals = append(vals, *s.Value)
	}
	return vals
}

func testThresholds(t *testing.T, d *dashboard, path string) {
	t.Helper()
	var panels []*panel
	allPanels(d.Panels, &panels)
	for _, p := range panels {
		vals := stepsValues(t, p)
		prev := float64(0)
		for _, v := range vals {
			if v <= prev {
				t.Errorf("%s: panel %d (%q): threshold values must be strictly increasing, got %v", path, p.ID, p.Title, vals)
				break
			}
			prev = v
		}
		if p.FieldConfig.Defaults.Unit == "percentunit" {
			// percentunit panels render values in the [0..1] range, so a
			// threshold above 1 (e.g. the Grafana default 80, which is 8000%)
			// would never be crossed and is meaningless.
			for _, v := range vals {
				if v < 0 || v > 1 {
					t.Errorf("%s: panel %d (%q): percentunit threshold values must be in the [0..1] range, got %v", path, p.ID, p.Title, vals)
					break
				}
			}
		}
	}
}

// TestDashboardsAreValidJSON ensures every shipped dashboard parses.
func TestDashboardsAreValidJSON(t *testing.T) {
	for name, path := range findDashboards(t) {
		loadDashboard(t, path)
		t.Logf("validated %s", name)
	}
}

// TestThresholdsAreSane ensures thresholds are increasing and, for percentunit
// panels, within the representable [0..1] range. This guards against the
// meaningless Grafana default thresholds (e.g. 80, which is 8000% for
// percentunit panels) creeping back into the dashboards. See #7603.
func TestThresholdsAreSane(t *testing.T) {
	for name, path := range findDashboards(t) {
		d := loadDashboard(t, path)
		t.Run(name, func(t *testing.T) {
			testThresholds(t, d, name)
		})
	}
}

// alertThresholds lists the resource usage panels whose thresholds are derived
// from the official alerting rules at deployment/docker/rules/ and from the
// capacity planning docs: dashboard file -> panel id -> expected step values.
var alertThresholds = map[string]map[int][]float64{
	"victoriametrics.json": {
		112: {0.5, 0.8},  // RSS memory % usage: TooHighMemoryUsage alert + 50% spare RAM
		114: {0.5, 0.9},  // CPU % usage: TooHighCPUUsage alert + 50% spare CPU
		123: {0.5, 0.8},  // RSS anonymous memory % usage
		142: {0.8, 0.95}, // Open FDs usage %: ProcessNearFDLimits alert
		145: {0.8},       // Disk space usage %: DiskRunsOutOfSpace alert
		90:  {0.8},       // Cache usage % by type
	},
	"victoriametrics-cluster.json": {
		66:  {0.5, 0.8},  // RSS memory % usage
		64:  {0.5, 0.9},  // CPU
		138: {0.5, 0.8},  // RSS anonymous memory % usage
		117: {0.8, 0.95}, // Open FDs usage %
		20:  {0.8},       // Disk space usage %: DiskRunsOutOfSpace alert
		144: {0.8},       // Cache usage % by type
	},
	"vmagent.json": {
		111: {0.5, 0.8},  // RSS memory % usage
		157: {0.5, 0.9},  // CPU % usage
		155: {0.5, 0.8},  // RSS anonymous memory % usage
		83:  {0.8, 0.95}, // Open FDs usage %
	},
	"vmalert.json": {
		37: {0.5, 0.8},  // Memory usage %
		35: {0.5, 0.9},  // CPU usage %
		65: {0.5, 0.8},  // RSS anonymous memory % usage
		39: {0.8, 0.95}, // Open FDs usage %
	},
	"vmauth.json": {
		25: {0.5, 0.8},  // RSS memory % usage
		26: {0.5, 0.9},  // CPU % usage
		20: {0.8, 0.95}, // Open FDs
	},
}

// TestResourcePanelThresholdsMatchAlerts ensures the resource usage panels keep
// the thresholds derived from the official alerting rules, so the dashboards
// continue to flag unhealthy deployments. See #7603.
func TestResourcePanelThresholdsMatchAlerts(t *testing.T) {
	for name, expected := range alertThresholds {
		d := loadDashboard(t, name)
		var panels []*panel
		allPanels(d.Panels, &panels)
		byID := make(map[int]*panel)
		for _, p := range panels {
			byID[p.ID] = p
		}
		for id, want := range expected {
			p, ok := byID[id]
			if !ok {
				t.Errorf("%s: panel %d not found", name, id)
				continue
			}
			got := stepsValues(t, p)
			if len(got) != len(want) {
				t.Errorf("%s: panel %d (%q): got threshold values %v, want %v", name, id, p.Title, got, want)
				continue
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s: panel %d (%q): got threshold values %v, want %v", name, id, p.Title, got, want)
					break
				}
			}
			// The thresholds must render on the graph, otherwise they have no
			// effect. "line" is the style the cluster dashboard already uses
			// for its per-node CPU and memory threshold lines.
			style := "off"
			if c := p.FieldConfig.Defaults.Custom; c != nil {
				style = c.ThresholdsStyle.Mode
			}
			if style == "off" {
				t.Errorf("%s: panel %d (%q): thresholdsStyle is %q, want it to render on the graph", name, id, p.Title, style)
			}
		}
	}
}

// TestVMDashboardsAreInSync ensures the VictoriaMetrics-datasource copies of
// the dashboards under the vm/ directory are in sync with the
// Prometheus-datasource originals. The copies are generated with
// `make dashboards-sync`.
func TestVMDashboardsAreInSync(t *testing.T) {
	for name := range alertThresholds {
		orig := loadDashboard(t, name)
		vm := loadDashboard(t, filepath.Join("vm", name))

		var origPanels, vmPanels []*panel
		allPanels(orig.Panels, &origPanels)
		allPanels(vm.Panels, &vmPanels)
		if len(origPanels) != len(vmPanels) {
			t.Errorf("%s: the vm copy has %d panels with thresholds, the original has %d; run `make dashboards-sync`", name, len(vmPanels), len(origPanels))
			continue
		}
		for i := range origPanels {
			op := origPanels[i]
			vp := vmPanels[i]
			if op.ID != vp.ID {
				t.Errorf("%s: panel order differs at index %d: %d vs %d; run `make dashboards-sync`", name, i, op.ID, vp.ID)
				continue
			}
			origJSON, err := json.Marshal(op.FieldConfig.Defaults.Thresholds)
			if err != nil {
				t.Fatalf("%s: cannot marshal thresholds for panel %d: %s", name, op.ID, err)
			}
			vmJSON, err := json.Marshal(vp.FieldConfig.Defaults.Thresholds)
			if err != nil {
				t.Fatalf("%s: cannot marshal thresholds for panel %d: %s", name, vp.ID, err)
			}
			if string(origJSON) != string(vmJSON) {
				t.Errorf("%s: panel %d (%q): thresholds in the vm copy differ from the original; run `make dashboards-sync`", name, op.ID, op.Title)
			}
			origStyle := thresholdsStyleMode(op)
			vmStyle := thresholdsStyleMode(vp)
			if origStyle != vmStyle {
				t.Errorf("%s: panel %d (%q): thresholdsStyle in the vm copy (%q) differs from the original (%q); run `make dashboards-sync`", name, op.ID, op.Title, vmStyle, origStyle)
			}
		}
	}
}

func thresholdsStyleMode(p *panel) string {
	if c := p.FieldConfig.Defaults.Custom; c != nil {
		return c.ThresholdsStyle.Mode
	}
	return ""
}
