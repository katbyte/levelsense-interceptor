package intercept

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/levelsense-interceptor/lib/cloud"
	"github.com/katbyte/levelsense-interceptor/lib/packet"
)

// report is a report shaped like the Level Sense Pro's, made up.
const report = `{"tempc":[-14.52,-14.61,-14.40],"rh":[41.2,41.0,41.5],"battvdc":4.12,"rssi":-61,"siren_state":0,"relay_state":0,"device_state":0,"alarm_silence":0}`

// success is what the cloud answers a report with when no settings are waiting.
const success = `{"result":"success","has_config_update":0}`

// fakeCloud answers every request with reply, or with err when it is set, and keeps the paths it was asked for.
type fakeCloud struct {
	mu    sync.Mutex
	reply cloud.Reply
	err   error
	paths []string
}

func (f *fakeCloud) Forward(_ context.Context, r *http.Request, _ []byte) (cloud.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.paths = append(f.paths, r.URL.Path)

	return f.reply, f.err
}

// fakeRaw keeps the lines written to it.
type fakeRaw struct {
	mu    sync.Mutex
	lines []string
}

func (f *fakeRaw) Write(line []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lines = append(f.lines, string(line))
}

// htmlReply is the cloud's answer, as it gives it: JSON, called HTML.
func htmlReply(body string) cloud.Reply {
	return cloud.Reply{Status: http.StatusOK, Header: http.Header{"Content-Type": {"text/html; charset=UTF-8"}}, Body: []byte(body)}
}

// reportFrom is the freezer's report of these readings, from 10.0.5.50 under this ID, running SENTRY_020122.
func reportFrom(t *testing.T, id, readings string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://cloud.level-sense.com"+packet.DataPath+"?json="+url.QueryEscape(readings), http.NoBody)
	// the freezer asks for the path alone, naming the host apart, where the test request names both
	r.RequestURI = r.URL.RequestURI()
	r.RemoteAddr = "10.0.5.50:49152"
	r.Header.Set("User-Agent", "SENTRY_020122")
	if id != "" {
		r.Header.Set("Device-Id", id)
	}

	return r
}

// A report is kept, counted and passed on, and the freezer is given the cloud's answer as it was.
func TestReport(t *testing.T) {
	t.Parallel()

	c := &fakeCloud{reply: htmlReply(success)}
	i := New(Options{Cloud: c})

	w := httptest.NewRecorder()
	i.ServeHTTP(w, reportFrom(t, "LS-0042", report))

	if w.Code != http.StatusOK || w.Body.String() != success || w.Header().Get("Content-Type") != "text/html; charset=UTF-8" || w.Header().Get("Content-Length") != strconv.Itoa(len(success)) {
		t.Errorf("the freezer was answered %d %v %q, want the cloud's answer as it was", w.Code, w.Header(), w.Body)
	}
	if len(c.paths) != 1 || c.paths[0] != packet.DataPath {
		t.Errorf("the cloud was asked for %v, want the report", c.paths)
	}

	s := i.Stats()
	if s.Requests[packet.DataPath] != 1 || s.Replies["200"] != 1 || s.LastReply.IsZero() {
		t.Errorf("counted %v requests and %v answers, the last at %v; want one of each", s.Requests, s.Replies, s.LastReply)
	}
	if len(s.Devices) != 1 {
		t.Fatalf("%d freezers known, want 1", len(s.Devices))
	}

	d := s.Devices[0]
	if d.ID != "LS-0042" || d.Name != "LS-0042" || d.Address != "10.0.5.50" || d.Firmware != "SENTRY_020122" || d.Reports != 1 || d.LastReport.IsZero() {
		t.Errorf("the freezer is %+v, want LS-0042, called by its ID, at 10.0.5.50 running SENTRY_020122 with one report", d)
	}
	if d.Readings.TemperatureC == nil || *d.Readings.TemperatureC != -14.52 || len(d.Numbers) != 12 {
		t.Errorf("the freezer read %v with %d numbers, want -14.52 and every number kept", d.Readings.TemperatureC, len(d.Numbers))
	}
	if d.ConfigWaiting == nil || *d.ConfigWaiting {
		t.Errorf("settings waiting = %v, want the cloud's no", d.ConfigWaiting)
	}

	i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0042", `{"tempc":[-15]}`))
	if d := i.Stats().Devices[0]; d.Reports != 2 || pointer.From(d.Readings.TemperatureC) != -15 || d.Readings.Humidity != nil {
		t.Errorf("after a second report the freezer has %d reports at %v, humidity %v; want the second report's readings alone", d.Reports, pointer.From(d.Readings.TemperatureC), d.Readings.Humidity)
	}
}

// A freezer named in the settings is called by its name beside its ID, one that is not by its ID alone, and a new firmware replaces the old.
func TestNames(t *testing.T) {
	t.Parallel()

	i := New(Options{Cloud: &fakeCloud{reply: htmlReply(success)}, Names: map[string]string{"LS-0042": "garage-freezer"}})
	i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0042", report))
	i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0043", report))

	updated := reportFrom(t, "LS-0042", report)
	updated.Header.Set("User-Agent", "SENTRY_020200")
	i.ServeHTTP(httptest.NewRecorder(), updated)

	s := i.Stats()
	if len(s.Devices) != 2 {
		t.Fatalf("%d freezers known, want 2", len(s.Devices))
	}
	if d := s.Devices[0]; d.ID != "LS-0042" || d.Name != "garage-freezer" || d.Firmware != "SENTRY_020200" {
		t.Errorf("the named freezer is %s called %q running %q, want LS-0042 called garage-freezer running SENTRY_020200", d.ID, d.Name, d.Firmware)
	}
	if d := s.Devices[1]; d.ID != "LS-0043" || d.Name != "LS-0043" {
		t.Errorf("the freezer not named is %s called %q, want it called by its ID", d.ID, d.Name)
	}
}

// An answer that says new settings are waiting says so for the freezer that reported; one that says nothing leaves it unknown.
func TestConfigWaiting(t *testing.T) {
	t.Parallel()

	for body, want := range map[string]string{`{"has_config_update":1}`: "true", `{"has_config_update":"0"}`: "false", `{"result":"success"}`: "unknown", "ok": "unknown"} {
		i := New(Options{Cloud: &fakeCloud{reply: htmlReply(body)}})
		i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0042", report))

		got := "unknown"
		if w := i.Stats().Devices[0].ConfigWaiting; w != nil {
			got = strconv.FormatBool(*w)
		}
		if got != want {
			t.Errorf("after the answer %s, settings waiting = %s, want %s", body, got, want)
		}
	}
}

// Every request is passed on, the freezer's others and a stranger's alike, and counted by what it asked for without keeping a freezer for it.
func TestRequestsWithoutAReport(t *testing.T) {
	t.Parallel()

	c := &fakeCloud{reply: htmlReply(`{"update_interval":"120"}`)}
	i := New(Options{Cloud: c})

	for _, target := range []string{packet.ConfigPath, "/favicon.ico", "/wp-login.php", packet.DataPath + "?json=not-json"} {
		w := httptest.NewRecorder()
		i.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://cloud.level-sense.com"+target, http.NoBody))
		if w.Body.String() != `{"update_interval":"120"}` {
			t.Errorf("%s was answered %q, want the cloud's answer", target, w.Body)
		}
	}

	s := i.Stats()
	if len(c.paths) != 4 || s.Requests[packet.ConfigPath] != 1 || s.Requests[OtherPath] != 2 || s.Requests[packet.DataPath] != 1 || len(s.Requests) != 3 {
		t.Errorf("passed on %v and counted %v; want all four, the strangers' under %s", c.paths, s.Requests, OtherPath)
	}
	if len(s.Devices) != 0 {
		t.Errorf("%d freezers known, want none: no request held a report that could be read", len(s.Devices))
	}
}

// When the cloud gives no answer the freezer is told so, the report is still kept, and the failure counted.
func TestCloudDown(t *testing.T) {
	t.Parallel()

	i := New(Options{Cloud: &fakeCloud{err: errors.New("connection refused")}})

	w := httptest.NewRecorder()
	i.ServeHTTP(w, reportFrom(t, "LS-0042", report))
	if w.Code != http.StatusBadGateway || w.Body.Len() != 0 {
		t.Errorf("the freezer was answered %d %q, want 502 and nothing more", w.Code, w.Body)
	}

	s := i.Stats()
	if s.Replies[Unreachable] != 1 || !s.LastReply.IsZero() || len(s.Devices) != 1 || s.Devices[0].ConfigWaiting != nil {
		t.Errorf("counted %v, last answer %v, %d freezers; want the failure counted, no answer, and the report kept", s.Replies, s.LastReply, len(s.Devices))
	}
}

// The answer reaches the freezer byte for byte over a real connection: no content type guessed for an answer without one, no chunks.
func TestAnswerOnTheWire(t *testing.T) {
	t.Parallel()

	i := New(Options{Cloud: &fakeCloud{reply: cloud.Reply{Status: http.StatusOK, Header: http.Header{}, Body: []byte(strings.Repeat("x", 9000))}}})
	srv := httptest.NewServer(i)
	t.Cleanup(srv.Close)

	var d net.Dialer
	conn, err := d.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if _, err := io.WriteString(conn, "GET /_device/data?json=%7B%7D HTTP/1.1\r\nHost: cloud.level-sense.com\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.Header.Get("Content-Type") != "" || len(res.TransferEncoding) != 0 || res.ContentLength != 9000 {
		t.Errorf("the freezer was sent %v, chunked %v, %d long; want no content type, no chunks, and the length", res.Header, res.TransferEncoding, res.ContentLength)
	}
}

// Past the most freezers kept, a new one's reports are passed on but not kept.
func TestMaxDevices(t *testing.T) {
	t.Parallel()

	c := &fakeCloud{reply: htmlReply(success)}
	i := New(Options{Cloud: c})
	for n := range maxDevices + 3 {
		i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, fmt.Sprintf("LS-%04d", n), report))
	}

	if got := len(i.Stats().Devices); got != maxDevices || len(c.paths) != maxDevices+3 {
		t.Errorf("%d freezers kept and %d reports passed on, want %d and every one", got, len(c.paths), maxDevices)
	}
}

// In offline mode the freezer is answered as the cloud answers it, the cloud is sent nothing, and the report is kept all the same. The freezer is
// never told settings are waiting, and is told there are none should it ask.
func TestOffline(t *testing.T) {
	t.Parallel()

	c := &fakeCloud{reply: htmlReply(`{"result":"success","has_config_update":1}`)}
	raw := &fakeRaw{}
	i := New(Options{Cloud: c, Raw: raw, Offline: true})

	w := httptest.NewRecorder()
	i.ServeHTTP(w, reportFrom(t, "LS-0042", report))
	if w.Code != http.StatusOK || w.Body.String() != success || w.Header().Get("Content-Type") != "text/html; charset=UTF-8" || w.Header().Get("Content-Length") != strconv.Itoa(len(success)) {
		t.Errorf("the freezer was answered %d %v %q, want the cloud's answer to a report", w.Code, w.Header(), w.Body)
	}

	for _, target := range []string{packet.ConfigPath, "/favicon.ico"} {
		w := httptest.NewRecorder()
		i.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://cloud.level-sense.com"+target, http.NoBody))
		if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("%s was answered %d %q, want 404 and nothing more", target, w.Code, w.Body)
		}
	}

	if len(c.paths) != 0 {
		t.Errorf("the cloud was asked for %v, want nothing", c.paths)
	}

	s := i.Stats()
	if !s.Offline || s.Requests[packet.DataPath] != 1 || s.Requests[packet.ConfigPath] != 1 || len(s.Replies) != 0 || !s.LastReply.IsZero() {
		t.Errorf("offline %t, counted %v requests and %v answers from the cloud, the last at %v; want the requests and nothing from the cloud", s.Offline, s.Requests, s.Replies, s.LastReply)
	}
	if len(s.Devices) != 1 || pointer.From(s.Devices[0].Readings.TemperatureC) != -14.52 || s.Devices[0].ConfigWaiting == nil || *s.Devices[0].ConfigWaiting {
		t.Fatalf("the freezers known are %+v, want the report kept and no settings waiting", s.Devices)
	}

	var e exchange
	if err := json.Unmarshal([]byte(raw.lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Reply == nil || !e.Reply.Local || e.Reply.Body != success {
		t.Errorf("the raw copy holds %+v, want the answer given here, marked local", e.Reply)
	}
}

// The raw copy holds each request and its answer as they were, one to a line.
func TestRaw(t *testing.T) {
	t.Parallel()

	raw := &fakeRaw{}
	i := New(Options{Cloud: &fakeCloud{reply: htmlReply(success)}, Raw: raw})
	i.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0042", report))

	down := New(Options{Cloud: &fakeCloud{err: errors.New("no route to host")}, Raw: raw})
	down.ServeHTTP(httptest.NewRecorder(), reportFrom(t, "LS-0042", report))

	if len(raw.lines) != 2 {
		t.Fatalf("the raw copy holds %d lines, want 2", len(raw.lines))
	}

	var e exchange
	if err := json.Unmarshal([]byte(raw.lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Method != http.MethodGet || !strings.HasPrefix(e.URI, packet.DataPath+"?json=") || e.Header.Get("Device-Id") != "LS-0042" || e.Reply == nil || e.Reply.Body != success {
		t.Errorf("the raw copy holds %+v, want the request and the cloud's answer", e)
	}

	e = exchange{}
	if err := json.Unmarshal([]byte(raw.lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Reply != nil || e.Error != "no route to host" {
		t.Errorf("with the cloud down the raw copy holds %+v, want the error and no answer", e)
	}
}
