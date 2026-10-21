package state

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/levelsense-interceptor/lib/intercept"
	"github.com/katbyte/levelsense-interceptor/lib/packet"
)

type stats intercept.Stats

func (s stats) Stats() intercept.Stats { return intercept.Stats(s) }

// lastReport is when both freezers last reported; the test's clock reads two minutes after.
var lastReport = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

// served is the state, for one named freezer reporting and one whose report held nothing, as Home Assistant would read it at path. The second is
// named the first's ID, which a name must never hide.
func served(t *testing.T, staleAfter time.Duration, path string) *httptest.ResponseRecorder {
	t.Helper()

	devices := []intercept.Device{
		{
			ID: "LS-0042", Name: "garage-freezer", MAC: "AA:BB", Address: "10.0.5.50", Firmware: "SENTRY_020122", Reports: 3, LastReport: lastReport,
			Values:        map[string]any{"tempc": []any{json.Number("-14.52")}},
			Readings:      packet.Readings{TemperatureC: pointer.To(-14.52), Siren: pointer.To(false)},
			ConfigWaiting: pointer.To(true),
		},
		{ID: "LS-0043", Name: "LS-0042", Address: "10.0.5.51", Reports: 1, LastReport: lastReport},
	}

	mux := http.NewServeMux()
	Mount(mux, Options{Source: stats{Devices: devices}, StaleAfter: staleAfter, Now: func() time.Time { return lastReport.Add(2 * time.Minute) }})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))

	return w
}

// One freezer is served at its own path, with its readings named, and null for each its report left out.
func TestOne(t *testing.T) {
	t.Parallel()

	w := served(t, 0, Path+"/LS-0042")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("answered %d %v: %s", w.Code, w.Header(), w.Body)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"device": "LS-0042", "name": "garage-freezer", "firmware": "SENTRY_020122", "online": true, "seconds_since_report": 120.0, "temperature_c": -14.52, "humidity_percent": nil, "siren": false, "config_waiting": true, "reports": 3.0}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %#v, want %#v", key, got[key], value)
		}
	}
	if values, ok := got["values"].(map[string]any); !ok || values["tempc"] == nil {
		t.Errorf("values = %v, want the report as it was sent", got["values"])
	}
}

// A freezer is found by its name as well as its ID, and an ID comes before a name.
func TestByName(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{Path + "/garage-freezer": "LS-0042", Path + "/LS-0043": "LS-0043", Path + "/LS-0042": "LS-0042"} {
		var got Device
		if err := json.Unmarshal(served(t, 0, path).Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got.Device != want {
			t.Errorf("%s served %q, want %s", path, got.Device, want)
		}
	}
}

// Every freezer is served at Path, by its ID, and one unheard from for longer than StaleAfter reads as offline.
func TestAll(t *testing.T) {
	t.Parallel()

	var all All
	if err := json.Unmarshal(served(t, time.Minute, Path).Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}

	if len(all.Devices) != 2 || all.Devices["LS-0043"].Address != "10.0.5.51" {
		t.Errorf("devices = %+v, want both by ID", all.Devices)
	}
	if all.Devices["LS-0042"].Online {
		t.Error("a freezer unheard from for two minutes reads as online, with a minute allowed")
	}
}

// A freezer that has not reported is not found.
func TestUnknown(t *testing.T) {
	t.Parallel()

	if w := served(t, 0, Path+"/LS-9999"); w.Code != http.StatusNotFound {
		t.Errorf("an unknown freezer answered %d, want 404", w.Code)
	}
}
