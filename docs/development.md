# Development

```bash
make            # format and build
make test       # the tests, under the race detector
make cover      # the tests with coverage
make docker     # the image
make check-all  # build, tests, every linter and the dependency check
make tools      # install the pinned dev tools into .tools/bin
```

Dependencies are vendored. The dev tools are pinned in `.tools/go.mod` and the makefile.

## Where things are

| Path | What it does |
|---|---|
| `lib/packet` | reads a report: which freezer sent it, and its readings |
| `lib/cloud` | passes a request on to the real cloud, looked up through DNS of its own, and brings back the answer; and the cloud's answer to a report, for offline mode |
| `lib/intercept` | what happens to each request, passed on or answered in offline mode, and what is known of each freezer |
| `lib/readings` | writes what is known as readings |
| `lib/state` | serves what is known as JSON, for Home Assistant |
| `lib/rawcopy` | keeps the requests and answers as they were |
| `cli` | the commands and settings |

The readings page, `/metrics`, `/healthz` and the labels are [go-kt](https://github.com/katbyte/go-kt)'s `prometheus/metrics`, shared with the other tools that serve one, and its tests use go-kt's `prometheus/metricstest`. Requests go on to the cloud through go-kt's `httpc`, sent once and never retried, since the freezer is waiting on the answer; `LEVELSENSE_INTERCEPTOR_LOG=TRACE` shows each request and answer. The logging, the HTTP serving and the version are go-kt's too.

## What the freezer sends

A Freezer Sentry's report has been seen, and has the Level Sense Pro's fields plus a few about its memory. What the Pro's fields mean comes from two community projects.

- **The report** is a GET of `/_device/data?json=<the readings as JSON>`, over plain HTTP, every five minutes. The JSON is read whether it was escaped once, not at all, or twice.
- **The answer** is short, `{"result":"success","has_config_update":0}`, sent as `text/html; charset=UTF-8`. A 1 means new settings are waiting. Offline mode gives this same answer, and never a 1.
- **The settings** are then fetched from `/_device/config`. The firmware reads them strictly: no spaces, keys in a set order. That is why answers are handed back untouched.
- **The freezer names itself** in the `Device-ID` and `MAC` headers, and its firmware in `User-Agent`, as `SENTRY_020122`, which is kept as its firmware. It also sends a login, the same on every device, which goes on to the cloud with the rest.

## The tests

`make test` needs no freezer, no cloud and no docker.

- The reports are made up, shaped like the Level Sense Pro's, with made-up IDs and addresses.
- The readings page is kept in [`lib/readings/testdata`](../lib/readings/testdata), for an interceptor that has heard from two freezers and for one that has heard nothing yet: a renamed reading or a changed label shows up before a dashboard or an alert finds it. `go test ./lib/readings -update` rewrites them.
- A made-up cloud takes the requests in the tests of `serve` and of the cloud. It can be made to redirect, to send a compressed answer, or not to be there at all.
- One test reads the answer off a real connection, to check nothing is added to it: no content type guessed, no chunks.
