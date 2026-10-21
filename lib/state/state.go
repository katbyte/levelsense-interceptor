// Package state serves what the interceptor knows of each freezer alarm as JSON, for Home Assistant's REST integration to read: one freezer at
// /state/{device}, by its ID or its name, every one at /state.
package state

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/levelsense-interceptor/lib/intercept"
)

const (
	// Path is where every freezer alarm is served, and Path/{device} one of them, by its ID or its name.
	Path = "/state"

	// DefaultStaleAfter is how long a freezer can go unheard before it reads as offline: it reports every five minutes, so fifteen is two missed.
	DefaultStaleAfter = 15 * time.Minute
)

// Source is what the state is read from: an *intercept.Interceptor.
type Source interface {
	Stats() intercept.Stats
}

// Options says what is served and how a freezer is judged offline.
type Options struct {
	Source Source
	// StaleAfter is how long a freezer can go unheard before it reads as offline; DefaultStaleAfter when 0.
	StaleAfter time.Duration
	// Now is the time, time.Now when nil: a test's own clock.
	Now func() time.Time
}

// Device is one freezer alarm as Home Assistant is given it. A reading its last report left out is null.
type Device struct {
	Device             string         `json:"device"`
	Name               string         `json:"name"`
	MAC                string         `json:"mac"`
	Address            string         `json:"address"`
	Firmware           string         `json:"firmware"`
	Online             bool           `json:"online"`
	LastReport         time.Time      `json:"last_report"`
	SecondsSinceReport float64        `json:"seconds_since_report"`
	Reports            uint64         `json:"reports"`
	TemperatureC       *float64       `json:"temperature_c"`
	HumidityPercent    *float64       `json:"humidity_percent"`
	BatteryVolts       *float64       `json:"battery_volts"`
	WifiSignalDBM      *float64       `json:"wifi_signal_dbm"`
	Siren              *bool          `json:"siren"`
	Relay              *bool          `json:"relay"`
	AlarmSilenced      *bool          `json:"alarm_silenced"`
	DeviceState        *float64       `json:"device_state"`
	ConfigWaiting      *bool          `json:"config_waiting"`
	Values             map[string]any `json:"values"`
}

// All is every freezer alarm, by its ID.
type All struct {
	Devices map[string]Device `json:"devices"`
}

// Mount adds the state to mux: every freezer at Path, one at Path/{device} by its ID or its name, and a 404 for a freezer that has not reported.
func Mount(mux *http.ServeMux, o Options) {
	if o.StaleAfter <= 0 {
		o.StaleAfter = DefaultStaleAfter
	}
	if o.Now == nil {
		o.Now = time.Now
	}

	mux.HandleFunc("GET "+Path, func(w http.ResponseWriter, _ *http.Request) {
		now := o.Now()
		all := All{Devices: map[string]Device{}}
		for _, d := range o.Source.Stats().Devices {
			all.Devices[d.ID] = of(d, now, o.StaleAfter)
		}

		write(w, all)
	})

	mux.HandleFunc("GET "+Path+"/{device}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("device")
		devices := o.Source.Stats().Devices

		// an ID is looked for first, so a name can never hide another freezer's ID
		n := slices.IndexFunc(devices, func(d intercept.Device) bool { return d.ID == key })
		if n < 0 {
			n = slices.IndexFunc(devices, func(d intercept.Device) bool { return d.Name == key })
		}
		if n < 0 {
			http.Error(w, "no report has come from the freezer alarm "+key, http.StatusNotFound)

			return
		}

		write(w, of(devices[n], o.Now(), o.StaleAfter))
	})
}

// of is the freezer alarm as Home Assistant is given it, at now.
func of(d intercept.Device, now time.Time, staleAfter time.Duration) Device {
	since := now.Sub(d.LastReport)

	return Device{
		Device:             d.ID,
		Name:               d.Name,
		MAC:                d.MAC,
		Address:            d.Address,
		Firmware:           d.Firmware,
		Online:             since <= staleAfter,
		LastReport:         d.LastReport,
		SecondsSinceReport: since.Round(time.Second).Seconds(),
		Reports:            d.Reports,
		TemperatureC:       d.Readings.TemperatureC,
		HumidityPercent:    d.Readings.Humidity,
		BatteryVolts:       d.Readings.BatteryVolts,
		WifiSignalDBM:      d.Readings.SignalDBM,
		Siren:              d.Readings.Siren,
		Relay:              d.Readings.Relay,
		AlarmSilenced:      d.Readings.AlarmSilenced,
		DeviceState:        d.Readings.DeviceState,
		ConfigWaiting:      d.ConfigWaiting,
		Values:             d.Values,
	}
}

// write sends v as JSON.
func write(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		clog.Log.Errorf("writing the state as JSON: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(b, '\n'))
}
