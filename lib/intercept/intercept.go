// Package intercept is what happens to each request a freezer alarm makes: its report is read, the request is passed on to the cloud unchanged, and
// the cloud's answer is handed back to the freezer exactly as it was given. In offline mode the freezer is answered here instead, as the cloud would
// answer it, and the cloud is told nothing. What the reports said, and how the cloud answered, is kept for the readings and for Home Assistant.
package intercept

import (
	"cmp"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/levelsense-interceptor/lib/cloud"
	"github.com/katbyte/levelsense-interceptor/lib/packet"
)

const (
	// OtherPath counts every request for a path the freezer is not known to ask for, as one, so a stranger's guesses cannot add to the page
	// without end.
	OtherPath = "other"
	// Unreachable is the status counted when the cloud gave no answer at all.
	Unreachable = "unreachable"

	// maxRequest is the most of a request's body that is read: the freezer sends its reports in the address, with no body.
	maxRequest = 1 << 20
	// maxDevices is the most freezer alarms kept track of, so a stranger making reports up under new names cannot fill the memory.
	maxDevices = 32
)

// Paths are the paths requests are counted under: the freezer's two, and everything else.
var Paths = []string{packet.DataPath, packet.ConfigPath, OtherPath}

// Forwarder passes a request on to the cloud: a *cloud.Cloud.
type Forwarder interface {
	Forward(ctx context.Context, r *http.Request, body []byte) (cloud.Reply, error)
}

// Recorder keeps a copy of each request and its answer: a *rawcopy.File.
type Recorder interface {
	Write(line []byte)
}

// Options says where requests go.
type Options struct {
	Cloud Forwarder
	// Raw, when set, keeps every request and answer as they were.
	Raw Recorder
	// Names are the names given to freezer alarms, by ID. One not named is called by its ID.
	Names map[string]string
	// Offline answers the freezer here, in place of the cloud, which is sent nothing: the freezer goes on reporting and its readings go no further.
	Offline bool
}

// Device is what is known of one freezer alarm, from its last report.
type Device struct {
	ID      string
	MAC     string
	Address string
	// Name is what the freezer alarm was named, or its ID when it was not.
	Name string
	// Firmware is the firmware the freezer said it runs, in its last report.
	Firmware   string
	Reports    uint64
	LastReport time.Time
	// Values are the readings as sent.
	Values   map[string]any
	Readings packet.Readings
	Numbers  []packet.Number
	// ConfigWaiting is whether the cloud's answer to the last report said new settings are waiting for the freezer, nil when it did not say.
	ConfigWaiting *bool
}

// Stats is a copy of everything known, safe to read while requests go on.
type Stats struct {
	// Requests counts the requests made, by path.
	Requests map[string]uint64
	// Replies counts the cloud's answers, by HTTP status, and the times it gave none as Unreachable.
	Replies map[string]uint64
	// LastReply is when the cloud last answered, and ReplyTook how long that answer took.
	LastReply time.Time
	ReplyTook time.Duration
	// Devices are the freezer alarms that have reported, by ID.
	Devices []Device
	// Offline is whether the freezer is answered here, in place of the cloud.
	Offline bool
}

// Interceptor answers the freezer's requests. It is safe to use from many goroutines.
type Interceptor struct {
	o Options

	mu        sync.Mutex
	requests  map[string]uint64
	replies   map[string]uint64
	lastReply time.Time
	replyTook time.Duration
	devices   map[string]*Device
	// failing is whether the cloud gave no answer the last time it was asked, so the log says when that starts and stops, not at every report
	failing bool
}

// New is an interceptor passing requests on as o says.
func New(o Options) *Interceptor {
	return &Interceptor{o: o, requests: map[string]uint64{}, replies: map[string]uint64{}, devices: map[string]*Device{}}
}

// ServeHTTP reads the request's report, if it has one, passes the request on to the cloud, and gives the freezer the cloud's answer. When the cloud
// gives none, the freezer is told so with a 502, as it would be by any proxy, and tries again at its next report.
func (i *Interceptor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	at := time.Now()

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequest))
	if err != nil {
		clog.Log.Warnf("reading a request from %s: %v", r.RemoteAddr, err)
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	device := i.noteRequest(r, at)

	if i.o.Offline {
		reply := local(r)
		i.noteLocal(device, reply)
		i.record(r, body, at, reply, nil)
		answer(w, reply)

		return
	}

	reply, err := i.o.Cloud.Forward(r.Context(), r, body)
	i.noteReply(device, reply, err)
	i.record(r, body, at, reply, err)

	if err != nil {
		w.WriteHeader(http.StatusBadGateway)

		return
	}

	answer(w, reply)
}

// local is the answer given in the cloud's place, in offline mode: a report is told it was taken, as the cloud tells it, and anything else that
// there is nothing here. The freezer is never told new settings are waiting, so it never asks for them, and keeps the ones it has.
func local(r *http.Request) cloud.Reply {
	if r.URL.Path == packet.DataPath {
		return cloud.Success()
	}

	if r.URL.Path == packet.ConfigPath {
		clog.Log.Warnf("%s asked for its settings, which are not kept here in offline mode: it is told there are none, and keeps the ones it has", r.RemoteAddr)
	}

	return cloud.Reply{Status: http.StatusNotFound, Header: http.Header{}}
}

// answer hands the cloud's answer to the freezer as it was given. The freezer's firmware reads it strictly, so nothing is added: Go would otherwise
// guess a content type for an answer without one, and split a long one into chunks.
func answer(w http.ResponseWriter, reply cloud.Reply) {
	h := w.Header()
	maps.Copy(h, reply.Header)
	if _, ok := reply.Header["Content-Type"]; !ok {
		h["Content-Type"] = nil
	}
	h.Set("Content-Length", strconv.Itoa(len(reply.Body)))

	w.WriteHeader(reply.Status)
	_, _ = w.Write(reply.Body)
}

// noteRequest counts the request and keeps its report, giving back which freezer sent it: "" for a request with no report.
func (i *Interceptor) noteRequest(r *http.Request, at time.Time) string {
	path := r.URL.Path
	if !slices.Contains(Paths, path) {
		path = OtherPath
	}

	p, isReport, err := packet.Read(r)
	if err != nil {
		clog.Log.Warnf("a report from %s could not be read, and is passed on to the cloud all the same: %v", r.RemoteAddr, err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	i.requests[path]++
	if !isReport {
		clog.Log.Debugf("%s asked for %s, which holds no report", r.RemoteAddr, r.URL.Path)

		return ""
	}

	d, known := i.devices[p.Device]
	if !known {
		if len(i.devices) >= maxDevices {
			clog.Log.Warnf("a report from %s, as %q, is not kept: %d freezer alarms are known already", p.Address, p.Device, maxDevices)

			return ""
		}

		d = &Device{ID: p.Device, Name: cmp.Or(i.o.Names[p.Device], p.Device), Firmware: p.Firmware}
		i.devices[p.Device] = d
		clog.Log.Infof("the first report from the freezer alarm %s, at %s, running %q", d.called(), p.Address, p.Firmware)
	}

	if d.Firmware != p.Firmware {
		clog.Log.Infof("the freezer alarm %s now runs %q, where it ran %q", d.called(), p.Firmware, d.Firmware)
	}

	d.MAC, d.Address, d.Firmware = p.MAC, p.Address, p.Firmware
	d.Reports++
	d.LastReport = at
	d.Values, d.Readings, d.Numbers = p.Values, p.Readings(), p.Numbers()
	clog.Log.Debugf("a report from %s: %s", p.Device, r.URL.RawQuery)

	return p.Device
}

// called is how the log names the freezer alarm: its ID, and its name when it has one.
func (d *Device) called() string {
	if d.Name == d.ID {
		return d.ID
	}

	return d.ID + " (" + d.Name + ")"
}

// noteReply counts the cloud's answer, and keeps what it said to the freezer that reported.
func (i *Interceptor) noteReply(device string, reply cloud.Reply, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if err != nil {
		i.replies[Unreachable]++
		if !i.failing {
			clog.Log.Warnf("the cloud is not answering, and the freezer is told so: %v", err)
		}
		i.failing = true

		return
	}

	if i.failing {
		clog.Log.Info("the cloud is answering again")
	}
	i.failing = false

	i.replies[strconv.Itoa(reply.Status)]++
	i.lastReply, i.replyTook = time.Now(), reply.Took

	if d, ok := i.devices[device]; ok {
		d.ConfigWaiting = configWaiting(reply.Body)
	}
}

// noteLocal keeps what the answer given in the cloud's place said to the freezer that reported.
func (i *Interceptor) noteLocal(device string, reply cloud.Reply) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if d, ok := i.devices[device]; ok {
		d.ConfigWaiting = configWaiting(reply.Body)
	}
}

// configWaiting is whether the cloud's answer to a report says new settings are waiting, {"result":"success","has_config_update":1}, and nil for an
// answer that does not say.
func configWaiting(body []byte) *bool {
	var a struct {
		HasConfigUpdate any `json:"has_config_update"`
	}
	if json.Unmarshal(body, &a) != nil {
		return nil
	}

	n, ok := packet.Float(a.HasConfigUpdate)
	if !ok {
		return nil
	}

	return new(n != 0)
}

// exchange is one request and its answer as the raw copy keeps them, one to a line.
type exchange struct {
	At     time.Time   `json:"at"`
	From   string      `json:"from"`
	Method string      `json:"method"`
	URI    string      `json:"uri"`
	Host   string      `json:"host"`
	Header http.Header `json:"header"`
	Body   string      `json:"body,omitempty"`
	Reply  *rawReply   `json:"reply,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// rawReply is the cloud's answer as the raw copy keeps it.
type rawReply struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
	Took   float64     `json:"took_seconds"`
	// Local is whether the answer was given here, in offline mode, in place of the cloud's.
	Local bool `json:"local,omitempty"`
}

// record adds the request and its answer to the raw copy, when one is kept.
func (i *Interceptor) record(r *http.Request, body []byte, at time.Time, reply cloud.Reply, err error) {
	if i.o.Raw == nil {
		return
	}

	e := exchange{At: at, From: r.RemoteAddr, Method: r.Method, URI: r.RequestURI, Host: r.Host, Header: r.Header, Body: string(body)}
	if err != nil {
		e.Error = err.Error()
	} else {
		e.Reply = &rawReply{Status: reply.Status, Header: reply.Header, Body: string(reply.Body), Took: reply.Took.Seconds(), Local: i.o.Offline}
	}

	line, merr := json.Marshal(e)
	if merr != nil {
		clog.Log.Errorf("writing a request from %s for the raw copy: %v", r.RemoteAddr, merr)

		return
	}

	i.o.Raw.Write(line)
}

// Stats is a copy of everything known.
func (i *Interceptor) Stats() Stats {
	i.mu.Lock()
	defer i.mu.Unlock()

	s := Stats{Requests: maps.Clone(i.requests), Replies: maps.Clone(i.replies), LastReply: i.lastReply, ReplyTook: i.replyTook, Devices: make([]Device, 0, len(i.devices)), Offline: i.o.Offline}
	// a report's values and numbers are replaced whole by the next, never changed, so the copy can share them
	for _, id := range slices.Sorted(maps.Keys(i.devices)) {
		s.Devices = append(s.Devices, *i.devices[id])
	}

	return s
}
