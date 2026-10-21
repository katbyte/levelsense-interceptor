package cloud

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

// uri is a report as the freezer might write it: escaped, and with characters a careless rewrite would escape again.
const uri = "/_device/data?json=%7B%22tempc%22%3A%5B-14.52%5D%7D&x={raw}&y=a+b"

// sent is what the fake cloud was sent.
type sent struct {
	method string
	uri    string
	host   string
	header http.Header
	body   string
}

// seen keeps what the fake cloud was sent, as its handler and the test both read it.
type seen struct {
	mu   sync.Mutex
	last sent
}

// get is what the fake cloud was last sent.
func (s *seen) get() sent {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last
}

// fakeCloud is a cloud that keeps what it is sent and answers with answer.
func fakeCloud(t *testing.T, answer http.HandlerFunc) (*Cloud, *seen) {
	t.Helper()

	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		s.mu.Lock()
		s.last = sent{method: r.Method, uri: r.RequestURI, host: r.Host, header: r.Header.Clone(), body: string(body)}
		s.mu.Unlock()

		answer(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{Host: srv.Listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}

	return c, s
}

// freezer is a request as the freezer makes it to cloud.level-sense.com.
func freezer(t *testing.T, method, target, body string) *http.Request {
	t.Helper()

	r := httptest.NewRequestWithContext(t.Context(), method, "http://"+DefaultHost+target, strings.NewReader(body))
	r.Header.Set("Device-Id", "LS-0042")
	r.Header.Set("User-Agent", "ESP32HTTPClient")
	r.Header.Set("Connection", "keep-alive, X-Hop")
	r.Header.Set("X-Hop", "this connection only")

	return r
}

// The cloud is sent what the freezer sent, address, host, headers and body, but what belongs to the one connection, and its answer comes back as it
// was given.
func TestForward(t *testing.T) {
	t.Parallel()

	c, got := fakeCloud(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.Header().Set("Connection", "close")
		_, _ = w.Write([]byte(`{"result":"success","has_config_update":0}`))
	})

	reply, err := c.Forward(t.Context(), freezer(t, http.MethodPost, uri, "a body"), []byte("a body"))
	if err != nil {
		t.Fatal(err)
	}
	s := got.get()

	if s.method != http.MethodPost || s.uri != uri || s.host != DefaultHost || s.body != "a body" {
		t.Errorf("the cloud was sent %s %s for %s with %q; want the freezer's request as it was, to %s", s.method, s.uri, s.host, s.body, DefaultHost)
	}
	if s.header.Get("Device-Id") != "LS-0042" || s.header.Get("User-Agent") != "ESP32HTTPClient" {
		t.Errorf("the cloud was sent the headers %v, want the freezer's own", s.header)
	}
	if s.header.Get("X-Hop") != "" {
		t.Error("the cloud was sent X-Hop, which the freezer said was for its connection only")
	}

	if reply.Status != http.StatusOK || string(reply.Body) != `{"result":"success","has_config_update":0}` || reply.Header.Get("Content-Type") != "text/html; charset=UTF-8" {
		t.Errorf("the answer was %d %v %q, want the cloud's as it gave it", reply.Status, reply.Header, reply.Body)
	}
	if reply.Header.Get("Connection") != "" || reply.Header.Get("Content-Length") != "" {
		t.Errorf("the answer kept the headers %v, which belong to the connection it came over", reply.Header)
	}
	if reply.Took <= 0 {
		t.Errorf("the answer took %v, want how long it took", reply.Took)
	}
}

// A freezer that sends no name for itself is passed on with none, not with Go's.
func TestForwardWithoutAUserAgent(t *testing.T) {
	t.Parallel()

	c, got := fakeCloud(t, func(http.ResponseWriter, *http.Request) {})

	r := freezer(t, http.MethodGet, uri, "")
	r.Header.Del("User-Agent")
	if _, err := c.Forward(t.Context(), r, nil); err != nil {
		t.Fatal(err)
	}

	if s := got.get(); s.header["User-Agent"] != nil {
		t.Errorf("the cloud was sent User-Agent %q, want none", s.header.Get("User-Agent"))
	}
}

// A redirect and a compressed answer are the cloud's answer to the freezer, passed back as they are: not followed, not unpacked.
func TestForwardLeavesTheAnswerAlone(t *testing.T) {
	t.Parallel()

	c, _ := fakeCloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/_device/data", http.StatusFound)

			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("not really gzip"))
	})

	reply, err := c.Forward(t.Context(), freezer(t, http.MethodGet, "/moved", ""), nil)
	if err != nil || reply.Status != http.StatusFound || reply.Header.Get("Location") != "/_device/data" {
		t.Errorf("a redirect came back as %d %v (%v), want the redirect itself", reply.Status, reply.Header, err)
	}

	r := freezer(t, http.MethodGet, uri, "")
	r.Header.Set("Accept-Encoding", "gzip")
	reply, err = c.Forward(t.Context(), r, nil)
	if err != nil || string(reply.Body) != "not really gzip" || reply.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("a compressed answer came back as %q %v (%v), want it as it was sent", reply.Body, reply.Header, err)
	}
}

// A cloud that cannot be reached is an error, which the freezer is told of.
func TestForwardUnreachable(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()

	c, err := New(Options{Host: closed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Forward(t.Context(), freezer(t, http.MethodGet, uri, ""), nil); err == nil {
		t.Error("a cloud nothing answers at gave no error")
	}
}

// A name found only on a private network is the local override pointing back here, and is refused, where an address given outright is used as it is.
func TestAddresses(t *testing.T) {
	t.Parallel()

	c, err := New(Options{Host: DefaultHost})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]netip.Addr{
		"back.here": {netip.MustParseAddr("10.0.0.12"), netip.MustParseAddr("127.0.0.1")},
		"mixed":     {netip.MustParseAddr("192.168.1.2"), netip.MustParseAddr("::ffff:104.192.6.192")},
	}
	c.lookup = func(_ context.Context, host string) ([]netip.Addr, error) { return found[host], nil }

	if addrs, err := c.addresses(t.Context(), "back.here"); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Errorf("a name found only here = %v, %v; want it refused", addrs, err)
	}

	addrs, err := c.addresses(t.Context(), "mixed")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "104.192.6.192" {
		t.Errorf("a name found here and away = %v, %v; want only the address away", addrs, err)
	}

	addrs, err = c.addresses(t.Context(), "10.0.0.12")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "10.0.0.12" {
		t.Errorf("an address given outright = %v, %v; want it as it is", addrs, err)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "http://cloud.level-sense.com", "cloud.level-sense.com/_device", "a host"} {
		if _, err := New(Options{Host: bad}); err == nil {
			t.Errorf("New(%q) gave no error", bad)
		}
	}

	c, err := New(Options{Host: DefaultHost, DNS: []string{"1.1.1.1", "8.8.8.8:5353"}})
	if err != nil || c.Host() != DefaultHost {
		t.Errorf("New = %v, %v", c, err)
	}
}
