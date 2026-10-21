package readings

import (
	"bytes"
	"testing"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/katbyte/levelsense-interceptor/lib/intercept"
	"github.com/katbyte/levelsense-interceptor/lib/packet"
)

type stats intercept.Stats

func (s stats) Stats() intercept.Stats { return intercept.Stats(s) }

// pageOf is the page written for these stats.
func pageOf(t *testing.T, s stats) string {
	t.Helper()

	p, err := metrics.New(metrics.Options{Name: "levelsense-interceptor"}, New(s))
	if err != nil {
		t.Fatal(err)
	}

	var page bytes.Buffer
	if err := p.Write(&page); err != nil {
		t.Fatal(err)
	}

	return page.String()
}

// reported is a freezer alarm, named in the settings, that has reported twice, the second time with the siren sounding.
var reported = intercept.Device{
	ID:         "LS-0042",
	Name:       "garage-freezer",
	MAC:        "AA:BB:CC:DD:EE:FF",
	Firmware:   "SENTRY_020122",
	Address:    "10.0.5.50",
	Reports:    2,
	LastReport: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC),
	Readings: packet.Readings{
		TemperatureC:  pointer.To(-14.52),
		Humidity:      pointer.To(41.2),
		BatteryVolts:  pointer.To(4.12),
		SignalDBM:     pointer.To(-61.0),
		DeviceState:   pointer.To(0.0),
		Siren:         pointer.To(true),
		Relay:         pointer.To(false),
		AlarmSilenced: pointer.To(false),
	},
	Numbers: []packet.Number{
		{Field: "battvdc", Value: 4.12},
		{Field: "tempc", Index: "0", Value: -14.52},
		{Field: "tempc", Index: "1", Value: -14.61},
		{Field: "tempc", Index: "2", Value: -14.4},
	},
	ConfigWaiting: pointer.To(false),
}

// The page is what dashboards and alerts are written against: go test -update rewrites testdata, to be read before it is kept.
func TestReadings(t *testing.T) {
	t.Parallel()

	page := pageOf(t, stats{
		Requests:  map[string]uint64{packet.DataPath: 30, packet.ConfigPath: 1, intercept.OtherPath: 2},
		Replies:   map[string]uint64{"200": 29, "502": 1, intercept.Unreachable: 1},
		LastReply: time.Date(2026, 10, 20, 9, 30, 0, 250000000, time.UTC),
		ReplyTook: 212 * time.Millisecond,
		Devices:   []intercept.Device{reported, {ID: "LS-0043", Name: "LS-0043", Address: "10.0.5.51", Reports: 1, LastReport: time.Date(2026, 10, 20, 9, 29, 0, 0, time.UTC)}},
	})

	metricstest.Saved(t, page, "testdata/reporting.prom")
	metricstest.Lint(t, page)

	// the saved page leaves out which build wrote it, so its name is checked here
	if n, ok := metricstest.One(t, page, "levelsense_interceptor_build_info", nil); !ok || n != 1 {
		t.Errorf("levelsense_interceptor_build_info = %v (on the page: %t), want 1", n, ok)
	}

	// a named freezer's readings carry its ID as well as its name, so naming it, or naming it again, keeps the ID to find it by
	if n, ok := metricstest.One(t, page, "levelsense_temperature_celsius", map[string]string{"device": "LS-0042", "name": "garage-freezer"}); !ok || n != -14.52 {
		t.Errorf("the temperature of LS-0042, named garage-freezer = %v (on the page: %t), want -14.52", n, ok)
	}

	// a freezer whose report held no readings has none on the page, rather than readings of 0
	if _, ok := metricstest.One(t, page, "levelsense_temperature_celsius", map[string]string{"device": "LS-0043"}); ok {
		t.Error("LS-0043 sent no temperature but has one on the page")
	}
}

// Before anything is heard each path and the usual answers are on the page, at 0, so an alert on the rate of reports fires for an interceptor the
// freezer has never reached.
func TestReadingsBeforeAnyReport(t *testing.T) {
	t.Parallel()

	page := pageOf(t, stats{})

	metricstest.Saved(t, page, "testdata/quiet.prom")
	if n, ok := metricstest.One(t, page, "levelsense_requests_total", map[string]string{"path": packet.DataPath}); !ok || n != 0 {
		t.Errorf("reports = %v (on the page: %t), want 0", n, ok)
	}
	if n, ok := metricstest.One(t, page, "levelsense_cloud_replies_total", map[string]string{"status": intercept.Unreachable}); !ok || n != 0 {
		t.Errorf("unreachable = %v (on the page: %t), want 0", n, ok)
	}
	if n, ok := metricstest.One(t, page, "levelsense_offline_mode", nil); !ok || n != 0 {
		t.Errorf("offline mode = %v (on the page: %t), want 0 for requests passed on to the cloud", n, ok)
	}
}

// In offline mode the page says so, so it is never mistaken for a cloud that has stopped answering.
func TestReadingsOffline(t *testing.T) {
	t.Parallel()

	if n, ok := metricstest.One(t, pageOf(t, stats{Offline: true}), "levelsense_offline_mode", nil); !ok || n != 1 {
		t.Errorf("offline mode = %v (on the page: %t), want 1", n, ok)
	}
}
