package packet

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

// report is a report shaped like the Level Sense Pro's, the only one anyone has published: made up, with its fields, lists of three and all.
const report = `{"sample_elapsed_ms":1234,"cap_sense":[1581,1580,1582],"cycle_count":17,"tempc":[-14.52,-14.61,-14.40],"rh":[41.2,41.0,41.5],"battvdc":4.12,"input1":[1430,1428,1431],"input2":[1430,1429,1430],"relay_state":0,"device_state":0,"rssi":-61,"siren_state":1,"alarm_silence":0,"debug_mode":0,"run_t":86400}`

// request is the freezer's request for this query, from 10.0.5.50 with these headers.
func request(t *testing.T, query string, header map[string]string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://cloud.level-sense.com"+DataPath+"?"+query, http.NoBody)
	r.RemoteAddr = "10.0.5.50:49152"
	for name, value := range header {
		r.Header.Set(name, value)
	}

	return r
}

func TestRead(t *testing.T) {
	t.Parallel()

	p, ok, err := Read(request(t, "json="+url.QueryEscape(report), map[string]string{"Device-ID": "LS-0042", "MAC": "AA:BB:CC:DD:EE:FF", "User-Agent": "SENTRY_020122"}))
	if err != nil || !ok {
		t.Fatalf("Read = %t, %v; want a report", ok, err)
	}

	if p.Device != "LS-0042" || p.MAC != "AA:BB:CC:DD:EE:FF" || p.Address != "10.0.5.50" || p.Firmware != "SENTRY_020122" {
		t.Errorf("the freezer is %q, MAC %q, at %q, running %q; want its ID, its MAC, its address without the port and its firmware", p.Device, p.MAC, p.Address, p.Firmware)
	}
	if got := fmt.Sprint(p.Values["battvdc"]); got != "4.12" {
		t.Errorf("battvdc = %v, want 4.12 as it was written", got)
	}
}

// The freezer is named by its ID, else its MAC address, else the address it reported from.
func TestReadNamesTheFreezer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		header map[string]string
		want   string
	}{
		{map[string]string{"Device-Id": "LS-0042", "Mac": "AA:BB"}, "LS-0042"},
		{map[string]string{"Mac": "AA:BB"}, "AA:BB"},
		{map[string]string{"Device-Id": "  "}, "10.0.5.50"},
		{nil, "10.0.5.50"},
	}
	for _, c := range cases {
		p, _, err := Read(request(t, "json=%7B%7D", c.header))
		if err != nil || p.Device != c.want {
			t.Errorf("with %v the freezer is %q (%v), want %q", c.header, p.Device, err, c.want)
		}
	}
}

// The JSON is read whether it was escaped once, as it should be, not at all, or twice over.
func TestReadEscaping(t *testing.T) {
	t.Parallel()

	once := url.QueryEscape(`{"tempc":[-14.5,-14.6]}`)
	for name, query := range map[string]string{"once": "json=" + once, "not at all": `json={"tempc":[-14.5,-14.6]}`, "twice": "json=" + url.QueryEscape(once)} {
		p, ok, err := Read(request(t, query, nil))
		if err != nil || !ok {
			t.Errorf("escaped %s: Read = %t, %v; want a report", name, ok, err)

			continue
		}
		if r := p.Readings(); r.TemperatureC == nil || *r.TemperatureC != -14.5 {
			t.Errorf("escaped %s: the temperature is %v, want -14.5", name, r.TemperatureC)
		}
	}
}

// A request without a report is none, which is not an error; one whose report is not JSON is.
func TestReadWithoutAReport(t *testing.T) {
	t.Parallel()

	if _, ok, err := Read(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://cloud.level-sense.com"+ConfigPath, http.NoBody)); ok || err != nil {
		t.Errorf("a settings request = %t, %v; want no report and no error", ok, err)
	}

	for _, bad := range []string{"json=tempc", "json=", "json=null", "json=%5B1%2C2%5D"} {
		if _, ok, err := Read(request(t, bad, nil)); ok || err == nil {
			t.Errorf("%s = %t, %v; want an error", bad, ok, err)
		}
	}
}

func TestReadings(t *testing.T) {
	t.Parallel()

	p, _, err := Read(request(t, "json="+url.QueryEscape(report), nil))
	if err != nil {
		t.Fatal(err)
	}

	r := p.Readings()
	numbers := map[string]*float64{"temperature": r.TemperatureC, "humidity": r.Humidity, "battery": r.BatteryVolts, "signal": r.SignalDBM, "state": r.DeviceState}
	want := map[string]float64{"temperature": -14.52, "humidity": 41.2, "battery": 4.12, "signal": -61, "state": 0}
	for name, n := range numbers {
		if n == nil || *n != want[name] {
			t.Errorf("%s = %v, want %v: the first of a list, or the number", name, n, want[name])
		}
	}

	if r.Siren == nil || !*r.Siren || r.Relay == nil || *r.Relay || r.AlarmSilenced == nil || *r.AlarmSilenced {
		t.Errorf("siren %v, relay %v, silenced %v; want on, off, off", r.Siren, r.Relay, r.AlarmSilenced)
	}

	// a report that leaves a reading out, or sends it as words, leaves it unknown rather than 0
	p, _, err = Read(request(t, "json="+url.QueryEscape(`{"rh":"dry","tempc":[],"siren_state":"1"}`), nil))
	if err != nil {
		t.Fatal(err)
	}
	r = p.Readings()
	if r.Humidity != nil || r.TemperatureC != nil || r.BatteryVolts != nil {
		t.Errorf("humidity %v, temperature %v, battery %v; want each unknown", r.Humidity, r.TemperatureC, r.BatteryVolts)
	}
	if r.Siren == nil || !*r.Siren {
		t.Errorf("a siren sent as \"1\" = %v, want on", r.Siren)
	}
}

// Every number is kept, field by field, so a reading nothing names yet still reaches the page.
func TestNumbers(t *testing.T) {
	t.Parallel()

	p, _, err := Read(request(t, "json="+url.QueryEscape(`{"tempc":[-14.5,"-14.6"],"rssi":-61,"fw":"1.2.3","on":true,"wifi":{"ch":[[6]]}}`), nil))
	if err != nil {
		t.Fatal(err)
	}

	want := []Number{
		{Field: "on", Value: 1},
		{Field: "rssi", Value: -61},
		{Field: "tempc", Index: "0", Value: -14.5},
		{Field: "tempc", Index: "1", Value: -14.6},
		{Field: "wifi.ch", Index: "0.0", Value: 6},
	}
	if got := p.Numbers(); !reflect.DeepEqual(got, want) {
		t.Errorf("Numbers =\n%v\nwant\n%v", got, want)
	}
}

func TestFloat(t *testing.T) {
	t.Parallel()

	for _, v := range []any{"NaN", "Inf", "", "1.2.3", nil, []any{1}, map[string]any{}} {
		if n, ok := Float(v); ok {
			t.Errorf("Float(%#v) = %v, want not a number", v, n)
		}
	}

	for v, want := range map[any]float64{" 15.00 ": 15, false: 0, 2.5: 2.5} {
		if n, ok := Float(v); !ok || n != want {
			t.Errorf("Float(%#v) = %v, %t; want %v", v, n, ok, want)
		}
	}
}
