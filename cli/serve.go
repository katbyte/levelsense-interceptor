package cli

import (
	"errors"
	"net/http"
	"os/signal"
	"sync"
	"syscall"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/httpd"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/levelsense-interceptor/lib/intercept"
	"github.com/katbyte/levelsense-interceptor/lib/state"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Take the freezer's requests, pass them on to the Level Sense cloud, and serve what they say",
		Long: `Takes the freezer alarm's requests on --listen and passes each on to the Level Sense
cloud at --cloud, unchanged, handing the cloud's answer back to the freezer as it was given.
The app, the website and the alerts go on working as before. When the cloud gives no answer,
the freezer is answered with a 502 and tries again at its next report.

For the freezer's requests to come here, the network's DNS has to answer
cloud.level-sense.com with this machine's address. That same DNS would send the
interceptor's own requests back to itself, so the cloud is looked up through --cloud-dns:
public DNS servers unless told otherwise.

What each report says is served at /metrics on --metrics-listen for Prometheus, and as JSON
at /state and /state/{device} for Home Assistant's REST integration, with a health probe at
/healthz. A freezer alarm unheard from for --stale-after reads as offline at /state.

A freezer alarm is known by the ID it sends. --names gives it a name as well, served at
/state/{name} and carried by its readings beside the ID.

--offline answers the freezer here, as the cloud would, and sends the cloud nothing. The
freezer goes on reporting, but the app and its alerts stop, and the cloud sees it go offline.

--raw-file also keeps every request and answer as they were, until it holds --raw-max-mb:
to see what the freezer really sends. It holds the freezer's MAC address and ID.

Every setting is also an environment variable: LEVELSENSE_INTERCEPTOR_LISTEN,
LEVELSENSE_INTERCEPTOR_METRICS_LISTEN, LEVELSENSE_INTERCEPTOR_CLOUD,
LEVELSENSE_INTERCEPTOR_CLOUD_DNS, LEVELSENSE_INTERCEPTOR_STALE_AFTER,
LEVELSENSE_INTERCEPTOR_RAW_FILE, LEVELSENSE_INTERCEPTOR_RAW_MAX_MB,
LEVELSENSE_INTERCEPTOR_LABELS, LEVELSENSE_INTERCEPTOR_NAMES and
LEVELSENSE_INTERCEPTOR_OFFLINE.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			c, err := f.upstream()
			if err != nil {
				return err
			}

			names, err := f.names()
			if err != nil {
				return err
			}

			opts := intercept.Options{Cloud: c, Names: names, Offline: f.Offline}
			raw, err := f.raw()
			if err != nil {
				return err
			}
			// set only when there is one: a nil file in the interface would not read as none
			if raw != nil {
				defer func() { _ = raw.Close() }()
				opts.Raw = raw
			}

			i := intercept.New(opts)
			page, err := f.page(i)
			if err != nil {
				return err
			}

			mux := http.NewServeMux()
			state.Mount(mux, state.Options{Source: i, StaleAfter: f.StaleAfter})
			mux.Handle("/", page.Handler())

			// one signal stops the intercepting and the serving
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			ln, err := httpd.Listen(ctx, f.Listen)
			if err != nil {
				return err
			}
			if f.Offline {
				clog.Log.Infof("taking the freezer's requests on %s and answering them here, in offline mode: the cloud is sent nothing", ln.Addr())
			} else {
				clog.Log.Infof("taking the freezer's requests on %s and passing them on to %s", ln.Addr(), c.Host())
			}

			var intercepting sync.WaitGroup
			var interceptErr error
			intercepting.Go(func() {
				interceptErr = httpd.Serve(ctx, ln, i)
				// the freezer's side can end by itself, and the rest has to end with it
				stop()
			})

			err = metrics.Serve(ctx, f.MetricsListen, mux)
			// serving can end by itself, on a port already taken say, and the rest has to end with it
			stop()
			intercepting.Wait()

			return errors.Join(err, interceptErr)
		},
	}
}
