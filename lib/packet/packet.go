// Package packet reads what a Level Sense freezer alarm reports: which unit it is, from its request's headers, and its readings, which it sends as
// JSON in the json part of the address it asks for.
package packet

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The paths the freezer asks the cloud for: the one it reports to, and the one it fetches its settings from when the cloud says new ones are waiting.
const (
	DataPath   = "/_device/data"
	ConfigPath = "/_device/config"
)

// Packet is one report from a freezer alarm.
type Packet struct {
	// Device names the unit: the ID it sends, else its MAC address, else the address it reported from.
	Device  string
	MAC     string
	Address string
	// Firmware is the firmware the unit says it runs, in its User-Agent: SENTRY_020122 on a Freezer Sentry.
	Firmware string
	// Values are the readings as sent, field by field, each number kept as it was written.
	Values map[string]any
}

// Read is the report a request carries, and false for one that carries none: the freezer fetching its settings, say. A report that cannot be read
// is an error.
func Read(r *http.Request) (Packet, bool, error) {
	sent, ok := r.URL.Query()["json"]
	if !ok {
		return Packet{}, false, nil
	}

	values, err := decode(sent[0])
	if err != nil {
		return Packet{}, false, fmt.Errorf("the report is not JSON: %w", err)
	}

	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		address = host
	}

	mac := strings.TrimSpace(r.Header.Get("Mac"))

	return Packet{Device: cmp.Or(strings.TrimSpace(r.Header.Get("Device-Id")), mac, address), MAC: mac, Address: address, Firmware: strings.TrimSpace(r.Header.Get("User-Agent")), Values: values}, true, nil
}

// decode reads the report's JSON. One escaped twice over, still written in %XX once the address is read, is read again unescaped.
func decode(s string) (map[string]any, error) {
	values, err := object(s)
	if err == nil {
		return values, nil
	}

	if unescaped, uerr := url.QueryUnescape(s); uerr == nil && unescaped != s {
		if values, uerr := object(unescaped); uerr == nil {
			return values, nil
		}
	}

	return nil, err
}

// object reads s as one JSON object, its numbers kept as they were written.
func object(s string) (map[string]any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()

	var values map[string]any
	if err := d.Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("it is null, not an object")
	}

	return values, nil
}

// Readings are the values in a report that are named here, each nil when the report left it out.
type Readings struct {
	TemperatureC  *float64
	Humidity      *float64
	BatteryVolts  *float64
	SignalDBM     *float64
	DeviceState   *float64
	Siren         *bool
	Relay         *bool
	AlarmSilenced *bool
}

// Readings names the values whose meaning is known from the Level Sense Pro, which sends the same fields. A field sent as a list of readings is read
// from its first, the current one by both community readings of the protocol; every one is still among Numbers.
func (p Packet) Readings() Readings {
	return Readings{
		TemperatureC:  p.first("tempc"),
		Humidity:      p.first("rh"),
		BatteryVolts:  p.first("battvdc"),
		SignalDBM:     p.first("rssi"),
		DeviceState:   p.first("device_state"),
		Siren:         flag(p.first("siren_state")),
		Relay:         flag(p.first("relay_state")),
		AlarmSilenced: flag(p.first("alarm_silence")),
	}
}

// first is the field's number, or the first of its list, nil when it has none.
func (p Packet) first(field string) *float64 {
	v := p.Values[field]
	if list, isList := v.([]any); isList && len(list) > 0 {
		v = list[0]
	}

	n, ok := Float(v)
	if !ok {
		return nil
	}

	return &n
}

// flag reads a number as on or off: anything but 0 is on.
func flag(n *float64) *bool {
	if n == nil {
		return nil
	}

	return new(*n != 0)
}

// Number is one number in a report, by the field it came in and its place in a list.
type Number struct {
	Field string
	// Index is the number's place in its field's list, "0" for the first; "0.1" for the second in the first list of a list. Empty for a field
	// that holds one number.
	Index string
	Value float64
}

// Numbers is every number in the report, field by field in order, so a reading nothing here names yet is still recorded. A field inside an object
// is named by its path, "wifi.rssi". Text that is not a number is left out.
func (p Packet) Numbers() []Number {
	var numbers []Number
	walk(p.Values, "", "", &numbers)

	return numbers
}

// walk adds the numbers in v, found under field and index, to numbers.
func walk(v any, field, index string, numbers *[]Number) {
	switch v := v.(type) {
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(v)) {
			walk(v[name], join(field, name), index, numbers)
		}
	case []any:
		for i, item := range v {
			walk(item, field, join(index, strconv.Itoa(i)), numbers)
		}
	default:
		if n, ok := Float(v); ok {
			*numbers = append(*numbers, Number{Field: field, Index: index, Value: n})
		}
	}
}

// join puts a dot between two parts of a name, when there are two.
func join(before, after string) string {
	if before == "" {
		return after
	}

	return before + "." + after
}

// Float reads a value from the freezer's JSON as a number: a number, a number written as text ("15.00", as the settings are), or true and false as 1
// and 0. Anything else, and a number too big to be one, is not.
func Float(v any) (float64, bool) {
	var n float64
	var err error

	switch v := v.(type) {
	case json.Number:
		n, err = v.Float64()
	case float64:
		n = v
	case string:
		n, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
	case bool:
		if v {
			return 1, true
		}

		return 0, true
	default:
		return 0, false
	}

	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}

	return n, true
}
