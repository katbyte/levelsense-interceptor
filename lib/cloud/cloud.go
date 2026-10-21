// Package cloud passes the freezer's requests on to the real Level Sense cloud and brings back its answers as they were given. The cloud's name is
// looked up through DNS servers of the interceptor's own, since the network's DNS has been told to point that name back at the interceptor.
package cloud

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/httpc"
)

// DefaultHost is where every Level Sense unit sends its reports, over plain HTTP.
const DefaultHost = "cloud.level-sense.com"

// DefaultDNS are the servers the cloud is looked up through: public ones, which know nothing of a local override pointing the name here.
var DefaultDNS = []string{"1.1.1.1:53", "8.8.8.8:53"}

const (
	// timeout is how long the cloud is given to answer. A freezer that hears nothing tries again at its next report.
	timeout = 10 * time.Second
	// maxBody is the most of an answer that is read: the cloud's run to a few hundred bytes.
	maxBody = 1 << 20
)

// Options says where the cloud is.
type Options struct {
	// Host is the cloud's name or address, with a port when it is not 80.
	Host string
	// DNS are the servers the name is looked up through, "1.1.1.1:53", tried in turn. None means the machine's own.
	DNS []string
}

// Reply is the cloud's answer, as it gave it.
type Reply struct {
	Status int
	Header http.Header
	Body   []byte
	// Took is how long the cloud took to answer.
	Took time.Duration
}

// Success is the cloud's answer to a report when it has no new settings for the freezer, as the real cloud gives it, for answering in its place.
func Success() Reply {
	return Reply{Status: http.StatusOK, Header: http.Header{"Content-Type": {"text/html; charset=UTF-8"}}, Body: []byte(`{"result":"success","has_config_update":0}`)}
}

// Cloud is the Level Sense cloud, reached as the freezer would reach it.
type Cloud struct {
	host   string
	client *httpc.Client
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

// New is the cloud at o.Host. Nothing is sent until a request is forwarded.
func New(o Options) (*Cloud, error) {
	if o.Host == "" || strings.ContainsAny(o.Host, "/?# ") {
		return nil, fmt.Errorf("%q is not a host: a name or an address, with a port when it is not 80", o.Host)
	}

	servers := make([]string, 0, len(o.DNS))
	for _, server := range o.DNS {
		if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(server, "53")
		}
		servers = append(servers, server)
	}

	c := &Cloud{host: o.Host, lookup: lookupThrough(servers)}
	transport := &http.Transport{
		// a proxy set for the machine is not the cloud
		Proxy:       nil,
		DialContext: c.dial,
		// the freezer's answer is passed on as the cloud wrote it, compressed or not
		DisableCompression:    true,
		ResponseHeaderTimeout: timeout,
		IdleConnTimeout:       90 * time.Second,
	}
	// sent once: the freezer is waiting on the answer, and tries again itself at its next report. LEVELSENSE_INTERCEPTOR_LOG=TRACE shows each exchange
	c.client = httpc.New(httpc.Options{Name: "Level Sense cloud", Log: clog.Log, Retry: httpc.Retry{Tries: 1}, HeaderWait: timeout, Base: transport})
	c.client.Timeout = timeout
	// a redirect is the cloud's answer to the freezer, not one to follow here
	c.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return c, nil
}

// Host is where the cloud is reached.
func (c *Cloud) Host() string { return c.host }

// Forward sends the request on to the cloud as the freezer sent it, address, headers and body alike, and gives back the cloud's answer. Only the
// headers that belong to the one connection are left behind, as any proxy leaves them.
func (c *Cloud) Forward(ctx context.Context, r *http.Request, body []byte) (Reply, error) {
	out, err := http.NewRequestWithContext(ctx, r.Method, "http://"+c.host+"/", bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("making the request to the cloud: %w", err)
	}

	// the path and query are copied as they arrived, not read and written again, so not one character of the report changes on the way
	out.URL.Path, out.URL.RawPath, out.URL.RawQuery = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
	out.Host = cmp.Or(r.Host, c.host)
	out.Header = passOn(r.Header)
	// a request without one would otherwise be sent with Go's own name in it
	if _, ok := out.Header["User-Agent"]; !ok {
		out.Header["User-Agent"] = []string{""}
	}

	start := time.Now()
	res, b, err := c.client.Fetch(out, maxBody) //nolint:bodyclose // Fetch hands the response back with its body read and closed
	if err != nil {
		return Reply{}, err
	}

	return Reply{Status: res.StatusCode, Header: passOn(res.Header), Body: b, Took: time.Since(start)}, nil
}

// hopByHop are the headers that belong to one connection and are not passed on. Content-Length is left for Go to write, from the body sent.
var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length"}

// passOn is a copy of the headers less those that belong to one connection, and any the Connection header names as such.
func passOn(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}

	for _, listed := range out.Values("Connection") {
		for name := range strings.SplitSeq(listed, ",") {
			out.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopByHop {
		out.Del(name)
	}

	return out
}

// dial connects to the cloud at one of the addresses its name is found at.
func (c *Cloud) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	addrs, err := c.addresses(ctx, host)
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: timeout}
	var failed []error
	for _, a := range addrs {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return conn, nil
		}
		failed = append(failed, err)
	}

	return nil, errors.Join(failed...)
}

// addresses are where host is: the host itself when it is an address, else where DNS finds it. A name found only on a private network is refused:
// the DNS asked points it back here, and the request would come back to the interceptor without end.
func (c *Cloud) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}

	found, err := c.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("looking up %s: %w", host, err)
	}

	var public []netip.Addr
	for _, a := range found {
		if a = a.Unmap(); !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() {
			public = append(public, a)
		}
	}

	if len(public) == 0 {
		return nil, fmt.Errorf("%s was found only at %v, on a private network: the DNS asked points it back at this network, not at the cloud", host, found)
	}

	return public, nil
}

// lookupThrough looks names up through these DNS servers, each in turn until one answers, or through the machine's own when there are none. Only
// IPv4 addresses are asked for: plenty of home networks have no IPv6 to reach the cloud over.
func lookupThrough(servers []string) func(ctx context.Context, host string) ([]netip.Addr, error) {
	if len(servers) == 0 {
		return func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		}
	}

	resolvers := make([]*net.Resolver, len(servers))
	for i, server := range servers {
		resolvers[i] = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		}}
	}

	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		var failed []error
		for i, r := range resolvers {
			addrs, err := r.LookupNetIP(ctx, "ip4", host)
			if err == nil {
				return addrs, nil
			}
			failed = append(failed, fmt.Errorf("through %s: %w", servers[i], err))
		}

		return nil, errors.Join(failed...)
	}
}
