package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/spf13/cobra"
)

// report is a report shaped like the Level Sense Pro's, made up, and success the cloud's answer to it.
const (
	report  = `{"tempc":[-14.52,-14.61,-14.40],"rh":[41.2,41.0,41.5],"battvdc":4.12,"rssi":-61,"siren_state":0}`
	success = `{"result":"success","has_config_update":0}`
)

// program is the program given these arguments, not yet run, and where what it prints will go; what it says went wrong is left out.
func program(t *testing.T, args ...string) (root *cobra.Command, stdout *bytes.Buffer) {
	t.Helper()

	root, err := Make()
	if err != nil {
		t.Fatalf("Make: %v", err)
	}

	stdout = &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(io.Discard)
	root.SetArgs(args)

	return root, stdout
}

// run executes the program with these arguments and gives back what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root, out := program(t, args...)
	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

//nolint:paralleltest // viper is global state; these mutate it
func TestVersion(t *testing.T) {
	alone(t)

	out, err := run(t, "version")
	if err != nil || !strings.HasPrefix(out, "levelsense-interceptor ") {
		t.Errorf("version = %q, %v", out, err)
	}

	out, err = run(t)
	if err != nil || !strings.Contains(out, "levelsense-interceptor help") {
		t.Errorf("no command = %q, %v; want a pointer to help", out, err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// fakeCloud is a Level Sense cloud that answers every report with success and keeps the addresses it was asked for.
type fakeCloud struct {
	mu   sync.Mutex
	uris []string
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.uris = append(f.uris, r.RequestURI)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	_, _ = w.Write([]byte(success))
}

func (f *fakeCloud) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.uris...)
}

// TestServe runs the interceptor as a deployment runs it: a report from the freezer reaches the cloud as it was sent and the cloud's answer comes
// back, its readings reach the page and the state for Home Assistant, the raw copy keeps it, and a signal stops it cleanly.
func TestServe(t *testing.T) {
	_, wd := alone(t)

	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)

	freezer, served := freeAddr(t), freeAddr(t)
	raw := filepath.Join(wd, "freezer.raw")
	t.Setenv("LEVELSENSE_INTERCEPTOR_CLOUD", srv.Listener.Addr().String())
	t.Setenv("LEVELSENSE_INTERCEPTOR_RAW_FILE", raw)

	ctx, cancel := context.WithCancel(t.Context())
	root, _ := program(t, "serve", "--listen", freezer, "--metrics-listen", served, "--labels", "hostname=dock", "--names", "LS-0042=garage-freezer")
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	if got := metricstest.Get(t, "http://"+served+metrics.HealthPath); got != "ok\n" {
		t.Fatalf("the health check said %q, want ok", got)
	}

	uri := "/_device/data?json=" + url.QueryEscape(report)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+freezer+uri, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "cloud.level-sense.com"
	req.Header.Set("Device-Id", "LS-0042")
	req.Header.Set("User-Agent", "SENTRY_020122")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK || string(body) != success || res.Header.Get("Content-Type") != "text/html; charset=UTF-8" {
		t.Errorf("the freezer was answered %d %v %q, want the cloud's answer", res.StatusCode, res.Header, body)
	}
	if asked := cloud.asked(); len(asked) != 1 || asked[0] != uri {
		t.Errorf("the cloud was asked for %v, want the freezer's request as it was sent", asked)
	}

	page := metricstest.Get(t, "http://"+served+metrics.Path)
	if n, ok := metricstest.One(t, page, "levelsense_temperature_celsius", map[string]string{"device": "LS-0042", "name": "garage-freezer", "hostname": "dock"}); !ok || n != -14.52 {
		t.Errorf("the temperature on the page = %v (on the page: %t), want -14.52 labelled with the freezer's ID, its name and the host", n, ok)
	}
	if _, ok := metricstest.One(t, page, "levelsense_device_info", map[string]string{"device": "LS-0042", "firmware": "SENTRY_020122"}); !ok {
		t.Error("the freezer's firmware is not on the page")
	}
	if n, ok := metricstest.One(t, page, "levelsense_cloud_replies_total", map[string]string{"status": "200"}); !ok || n != 1 {
		t.Errorf("the cloud's answers on the page = %v (on the page: %t), want 1", n, ok)
	}

	var state map[string]any
	if err := json.Unmarshal([]byte(metricstest.Get(t, "http://"+served+"/state/garage-freezer")), &state); err != nil {
		t.Fatal(err)
	}
	if online, ok := state["online"].(bool); state["device"] != "LS-0042" || state["temperature_c"] != -14.52 || !ok || !online {
		t.Errorf("the state for Home Assistant, by the freezer's name = %v, want its ID, the temperature and online", state)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve stopped with %v, want a clean stop", err)
	}

	kept, err := os.ReadFile(raw) //nolint:gosec // a file the test made
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), `"uri":"`+strings.ReplaceAll(uri, "&", `&`)+`"`) || !strings.Contains(string(kept), `"has_config_update\":0`) {
		t.Errorf("the raw copy holds %s, want the request and the answer", kept)
	}
}

// TestServeOffline runs the interceptor in offline mode: the freezer is answered as the cloud answers it, the cloud is never asked, and the report
// still reaches the page.
func TestServeOffline(t *testing.T) {
	alone(t)

	cloud := &fakeCloud{}
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)

	freezer, served := freeAddr(t), freeAddr(t)
	t.Setenv("LEVELSENSE_INTERCEPTOR_CLOUD", srv.Listener.Addr().String())

	ctx, cancel := context.WithCancel(t.Context())
	root, _ := program(t, "serve", "--listen", freezer, "--metrics-listen", served, "--offline")
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	if got := metricstest.Get(t, "http://"+served+metrics.HealthPath); got != "ok\n" {
		t.Fatalf("the health check said %q, want ok", got)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+freezer+"/_device/data?json="+url.QueryEscape(report), http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "cloud.level-sense.com"
	req.Header.Set("Device-Id", "LS-0042")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK || string(body) != success || res.Header.Get("Content-Type") != "text/html; charset=UTF-8" {
		t.Errorf("the freezer was answered %d %v %q, want the cloud's answer to a report", res.StatusCode, res.Header, body)
	}
	if asked := cloud.asked(); len(asked) != 0 {
		t.Errorf("the cloud was asked for %v, want nothing", asked)
	}

	page := metricstest.Get(t, "http://"+served+metrics.Path)
	if n, ok := metricstest.One(t, page, "levelsense_offline_mode", nil); !ok || n != 1 {
		t.Errorf("offline mode on the page = %v (on the page: %t), want 1", n, ok)
	}
	if n, ok := metricstest.One(t, page, "levelsense_temperature_celsius", map[string]string{"device": "LS-0042"}); !ok || n != -14.52 {
		t.Errorf("the temperature on the page = %v (on the page: %t), want -14.52", n, ok)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve stopped with %v, want a clean stop", err)
	}
}

// A port that cannot be listened on stops serve with an error, whichever of the two it is.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestServePortTaken(t *testing.T) {
	alone(t)

	var lc net.ListenConfig
	taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })

	if _, err := run(t, "serve", "--listen", freeAddr(t), "--metrics-listen", taken.Addr().String()); err == nil {
		t.Error("serve with its metrics port taken gave no error")
	}

	if _, err := run(t, "serve", "--listen", taken.Addr().String(), "--metrics-listen", freeAddr(t)); err == nil {
		t.Error("serve with the freezer's port taken gave no error")
	}
}

// Settings that cannot be used stop serve before it listens, naming the setting.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestServeBadSettings(t *testing.T) {
	alone(t)

	if _, err := run(t, "serve", "--cloud-dns", ","); err == nil || !strings.Contains(err.Error(), "LEVELSENSE_INTERCEPTOR_CLOUD_DNS") {
		t.Errorf("serve with no DNS server = %v, want an error naming the setting", err)
	}
}
