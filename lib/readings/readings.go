// Package readings writes what the interceptor knows as Prometheus readings, read fresh at each scrape: what each freezer alarm last reported, and
// how its requests and the cloud's answers have gone.
package readings

import (
	"maps"
	"slices"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/levelsense-interceptor/lib/intercept"
	"github.com/prometheus/client_golang/prometheus"
)

// namespace starts the name of every reading.
const namespace = "levelsense"

// list is every reading the page can hold.
var (
	list = metrics.NewList(namespace)

	requests  = list.Reading("requests_total", "Requests the freezer alarms made, by the path asked for: their reports, their settings, and any other.", "path")
	replies   = list.Reading("cloud_replies_total", "Answers from the Level Sense cloud, by HTTP status; unreachable for the times it gave none.", "status")
	replyTook = list.Reading("cloud_reply_seconds", "How long the cloud took to give its last answer.")
	lastReply = list.Reading("cloud_last_reply_timestamp_seconds", "When the cloud last answered, in seconds since 1970.")
	offline   = list.Reading("offline_mode", "Whether the freezer alarms are answered here and the cloud sent nothing (1), or their requests are passed on to the cloud (0).")

	info          = list.Reading("device_info", "Always 1, with the freezer alarm's MAC address, the firmware it runs, and the address it last reported from.", "device", "name", "mac", "address", "firmware")
	reports       = list.Reading("reports_total", "Reports the freezer alarm has sent.", "device", "name")
	lastReport    = list.Reading("last_report_timestamp_seconds", "When the freezer alarm last reported, in seconds since 1970.", "device", "name")
	temperature   = list.Reading("temperature_celsius", "The temperature the freezer alarm last reported, as it sent it.", "device", "name")
	humidity      = list.Reading("humidity_percent", "The relative humidity the freezer alarm last reported.", "device", "name")
	battery       = list.Reading("battery_volts", "The battery voltage the freezer alarm last reported.", "device", "name")
	signal        = list.Reading("wifi_signal_dbm", "The Wi-Fi signal strength the freezer alarm last reported.", "device", "name")
	siren         = list.Reading("siren_on", "Whether the freezer alarm's siren is sounding (1) or not (0).", "device", "name")
	relay         = list.Reading("relay_on", "Whether the freezer alarm's relay is switched on (1) or not (0).", "device", "name")
	silenced      = list.Reading("alarm_silenced", "Whether the freezer alarm's alarm has been silenced (1) or not (0).", "device", "name")
	state         = list.Reading("device_state", "The state number the freezer alarm reports: 0 in normal running, and what else it can mean is not documented.", "device", "name")
	configWaiting = list.Reading("config_waiting", "Whether the cloud's answer to the last report said new settings are waiting for the freezer alarm (1) or not (0).", "device", "name")
	value         = list.Reading("value", "Every number in the freezer alarm's last report, by the field it came in and its place in a list: those named above and those not.", "device", "name", "field", "index")
)

// statuses are the cloud's answers that are on the page from the start, at 0 until one is given, so a rate over them reads 0 rather than nothing.
var statuses = []string{"200", intercept.Unreachable}

// Source is what the readings are read from: an *intercept.Interceptor.
type Source interface {
	Stats() intercept.Stats
}

type collector struct {
	source Source
}

// New is the collector of the source's readings.
func New(source Source) prometheus.Collector {
	return collector{source: source}
}

func (collector) Describe(ch chan<- *prometheus.Desc) { list.Describe(ch) }

func (c collector) Collect(ch chan<- prometheus.Metric) {
	s := c.source.Stats()
	w := metrics.NewWriter(ch)

	for _, path := range intercept.Paths {
		w.Total(requests, s.Requests[path], path)
	}

	// the writer leaves off the ones already written
	for _, status := range slices.Concat(statuses, slices.Sorted(maps.Keys(s.Replies))) {
		w.Total(replies, s.Replies[status], status)
	}

	if !s.LastReply.IsZero() {
		w.Gauge(replyTook, s.ReplyTook.Seconds())
		w.At(lastReply, s.LastReply)
	}

	w.Flag(offline, s.Offline)

	for _, d := range s.Devices {
		device(w, d)
	}
}

// device writes one freezer alarm's readings, each with its ID and its name. A reading its last report left out is left off the page, which is not
// the same as a reading of 0.
func device(w *metrics.Writer, d intercept.Device) {
	w.Gauge(info, 1, d.ID, d.Name, d.MAC, d.Address, d.Firmware)
	w.Total(reports, d.Reports, d.ID, d.Name)
	w.At(lastReport, d.LastReport, d.ID, d.Name)

	gauge(w, temperature, d.Readings.TemperatureC, d.ID, d.Name)
	gauge(w, humidity, d.Readings.Humidity, d.ID, d.Name)
	gauge(w, battery, d.Readings.BatteryVolts, d.ID, d.Name)
	gauge(w, signal, d.Readings.SignalDBM, d.ID, d.Name)
	gauge(w, state, d.Readings.DeviceState, d.ID, d.Name)

	flag(w, siren, d.Readings.Siren, d.ID, d.Name)
	flag(w, relay, d.Readings.Relay, d.ID, d.Name)
	flag(w, silenced, d.Readings.AlarmSilenced, d.ID, d.Name)
	flag(w, configWaiting, d.ConfigWaiting, d.ID, d.Name)

	for _, n := range d.Numbers {
		w.Gauge(value, n.Value, d.ID, d.Name, n.Field, n.Index)
	}
}

// gauge writes the reading when there is one.
func gauge(w *metrics.Writer, d *prometheus.Desc, v *float64, labels ...string) {
	if v != nil {
		w.Gauge(d, *v, labels...)
	}
}

// flag writes the yes or no when there is one.
func flag(w *metrics.Writer, d *prometheus.Desc, v *bool, labels ...string) {
	if v != nil {
		w.Flag(d, *v, labels...)
	}
}
