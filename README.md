# levelsense-interceptor

[![GitHub release](https://img.shields.io/github/v/release/katbyte/levelsense-interceptor?color=blueviolet)](https://github.com/katbyte/levelsense-interceptor/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/levelsense-interceptor?color=00ADD8)](https://github.com/katbyte/levelsense-interceptor/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/levelsense-interceptor?color=blue)](https://github.com/katbyte/levelsense-interceptor/blob/main/LICENSE)
![build](https://github.com/katbyte/levelsense-interceptor/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/levelsense-interceptor/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/levelsense-interceptor/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/levelsense-interceptor/badges/coverage.json)](https://github.com/katbyte/levelsense-interceptor/actions/workflows/coverage.yaml)

A small program that sits between a [Level Sense](https://www.level-sense.com/) freezer alarm and the Level Sense cloud. It reads the freezer's reports on the way through and serves them to Prometheus and Home Assistant, while passing everything on to the cloud unchanged.

**Why.** The freezer alarm has no API of its own: nothing on it answers on any port. It only sends its readings out, over plain HTTP, to `cloud.level-sense.com`. Pointing that name at this program in your DNS puts it in the middle, so the temperature reaches your graphs and Home Assistant while the app and its alerts go on working.

**How it behaves.** Each request is passed on to the real cloud as the freezer sent it, and the cloud's answer is handed back as the cloud gave it, byte for byte. When the cloud cannot be reached, the freezer is told so and tries again at its next report. The cloud then raises its own "device offline" alert, as it would with nothing in the middle. In [offline mode](#offline-mode) the freezer is answered here instead, and the cloud is sent nothing.

**What is known, and what is not.** A Freezer Sentry sends the same fields as the Level Sense Pro, its sump-pump sibling, plus a few about its own memory. What the Pro's fields mean is known through two community projects ([one](https://github.com/KruseLuds/level_sense_pro_observer), [two](https://github.com/fzurita/ha_level_sense_pro)), so:

- Every number in each report is also served as `levelsense_value`, by field, whatever it is called. A reading named wrongly here is not lost.
- The temperature is served as the freezer sends it. The Pro's sensor sits inside its warm case and reads about 3.61°C high, which those projects take off. The Freezer Sentry's probe hangs inside the freezer on a cable, so no correction is made here. Check it against a thermometer.
- The one Freezer Sentry seen so far sends a battery voltage near 0. Whether it had no battery in, or the field means something else on this model, is not known yet.
- `LEVELSENSE_INTERCEPTOR_RAW_FILE` keeps every request and answer as they were, to see what yours really sends.

## Install

```bash
docker build -t levelsense-interceptor .
```

or `go install github.com/katbyte/levelsense-interceptor@latest`.

## Setting it up

1. **Run it on a host where port 80 is free.** [`docker-compose.yml`](docker-compose.yml) builds and runs it, taking the freezer's requests on port 80 and serving the readings on 9125. A reverse proxy already on port 80 that sends everything to HTTPS will not do: the freezer only speaks plain HTTP.

   ```bash
   docker compose up -d
   curl -s http://127.0.0.1:9125/healthz
   ```

2. **Point `cloud.level-sense.com` at that host in your DNS.** Override that one name, not all of `level-sense.com`, so the website and app go on working. If your network hands out two DNS servers, add it to both, or the freezer will sometimes go around it.

   | DNS server | How |
   |---|---|
   | Technitium | add a primary zone named `cloud.level-sense.com`, then an A record for `@` |
   | Pi-hole | Local DNS, DNS Records: `cloud.level-sense.com` |
   | AdGuard Home | Filters, DNS rewrites: `cloud.level-sense.com` |

3. **Wait for a report**, up to five minutes, or press RESET on the freezer alarm to have it reconnect. With `LEVELSENSE_INTERCEPTOR_LOG=INFO` the log says `the first report from the freezer alarm ...`, and the freezer appears at `/state`:

   ```bash
   curl -s http://127.0.0.1:9125/state
   ```

**Its own DNS.** The interceptor uses the same network, so your DNS would point the cloud's name back at it as well. It looks the cloud up through public DNS servers instead, `1.1.1.1` and `8.8.8.8`, and refuses a name found only on a private network rather than sending requests back to itself.

**To take it out of the middle**, remove the DNS override. The freezer goes back to the cloud on its next lookup.

## Offline mode

`LEVELSENSE_INTERCEPTOR_OFFLINE=true` answers the freezer here, the way the cloud does, and sends the cloud nothing. The freezer stays connected and goes on reporting, and its readings go no further than this program. It is off unless it is set.

- **The cloud sees the freezer go offline.** It gets no readings, but it notices the reports stop, and raises its own "device offline" alert. Turn the app's alerts off, or take the freezer off the account.
- **The app's alerts stop.** Nothing tells you the freezer is warming up unless you set it up yourself, in Grafana or Home Assistant, on the temperature and on the freezer going quiet. The freezer's own siren should go on working, since it sounds on the limits it already has, but that has not been tried yet.
- **Settings changed in the app do not reach the freezer.** It is never told new settings are waiting, so it keeps the ones it has. To change one, turn offline mode off until the freezer has fetched it, then on again. No firmware update is offered either.
- **`levelsense_offline_mode` is 1** while it is on, and the raw copy marks each answer given here as `local`.

Run it passing everything on for a day or two first, with a raw copy kept. Anything the freezer does now and then besides reporting shows up there, before offline mode has to answer it.

## Home Assistant

The state of each freezer alarm is served as JSON at `/state/{device}`, for Home Assistant's [REST integration](https://www.home-assistant.io/integrations/rest/). `{device}` is the ID the freezer sends, else its MAC address, else its IP address; `/state` lists them all. A reading the last report left out is `null`, and `online` turns false once the freezer has gone `LEVELSENSE_INTERCEPTOR_STALE_AFTER` without reporting.

**Naming a freezer.** The ID is a string of letters and digits the freezer makes up, such as `a1b2c3d4e5f6g7h8i9j0`. `LEVELSENSE_INTERCEPTOR_NAMES=a1b2c3d4e5f6g7h8i9j0=garage-freezer` gives it a name as well: it is then served at `/state/garage-freezer` too, and its readings carry `name="garage-freezer"` beside its ID. Pick a name once: a new one starts new lines in Grafana.

```yaml
rest:
  - resource: http://interceptor-host:9125/state/garage-freezer
    scan_interval: 60
    sensor:
      - name: Freezer temperature
        value_template: "{{ value_json.temperature_c }}"
        device_class: temperature
        unit_of_measurement: "°C"
        state_class: measurement
      - name: Freezer humidity
        value_template: "{{ value_json.humidity_percent }}"
        device_class: humidity
        unit_of_measurement: "%"
        state_class: measurement
    binary_sensor:
      - name: Freezer alarm online
        value_template: "{{ value_json.online }}"
        device_class: connectivity
      - name: Freezer siren
        value_template: "{{ value_json.siren }}"
        device_class: sound
```

Home Assistant keeps ten days of history by default. The readings below are for keeping more, in Prometheus or VictoriaMetrics and Grafana.

## Settings

None have to be given. Each can be a flag, an environment variable, or a line in a settings file.

| Variable | Flag | Default | What it sets |
|---|---|---|---|
| `LEVELSENSE_INTERCEPTOR_LISTEN` | `--listen` | `:80`, `:8080` in the image | where the freezer's requests are taken |
| `LEVELSENSE_INTERCEPTOR_METRICS_LISTEN` | `--metrics-listen` | `:9125` | where `/metrics`, `/state` and `/healthz` are served |
| `LEVELSENSE_INTERCEPTOR_CLOUD` | `--cloud` | `cloud.level-sense.com` | the cloud requests are passed on to: a name or an address, with a port when it is not 80 |
| `LEVELSENSE_INTERCEPTOR_CLOUD_DNS` | `--cloud-dns` | `1.1.1.1:53,8.8.8.8:53` | the DNS servers the cloud is looked up through, in turn, or `system` for the machine's own |
| `LEVELSENSE_INTERCEPTOR_STALE_AFTER` | `--stale-after` | `15m` | how long a freezer can go without reporting before `/state` says it is offline: it reports every five minutes |
| `LEVELSENSE_INTERCEPTOR_RAW_FILE` | `--raw-file` | none | also keep every request and answer, as they were, in this file |
| `LEVELSENSE_INTERCEPTOR_RAW_MAX_MB` | `--raw-max-mb` | `100` | stop adding to the raw copy at this size |
| `LEVELSENSE_INTERCEPTOR_LABELS` | `--labels` | none | labels for every reading, as `name=value,name=value` |
| `LEVELSENSE_INTERCEPTOR_NAMES` | `--names` | none | names for the freezers, as `id=name,id=name`; one not named is called by its ID |
| `LEVELSENSE_INTERCEPTOR_OFFLINE` | `--offline` | `false` | answer the freezer here and send the cloud nothing: see [offline mode](#offline-mode) |
| `LEVELSENSE_INTERCEPTOR_LOG` | | `WARN` | log level: `INFO` says when a freezer first reports and when the cloud stops and starts answering, `DEBUG` shows every report, and `TRACE` every exchange with the cloud |
| `LEVELSENSE_INTERCEPTOR_CONFIG` | `--config` | | the one settings file to read |

The settings file is `./.levelsense-interceptor` or `~/.levelsense-interceptor`. Its keys are the variable names without `LEVELSENSE_INTERCEPTOR_`:

```env
CLOUD_DNS=9.9.9.9:53
LABELS=hostname=dock
```

A flag or an environment variable wins over the file.

## Readings

Served at `/metrics` on port 9125. Every reading starts `levelsense_`, and those of one freezer alarm carry its ID as `device` and its name as `name`, which is its ID again when it has not been named. A reading its last report left out is left off the page, rather than shown as 0.

| Reading | Labels | What it says |
|---|---|---|
| `levelsense_temperature_celsius` | `device`, `name` | the temperature, as the freezer sent it |
| `levelsense_humidity_percent` | `device`, `name` | the relative humidity |
| `levelsense_battery_volts` | `device`, `name` | the battery voltage |
| `levelsense_wifi_signal_dbm` | `device`, `name` | the Wi-Fi signal strength |
| `levelsense_siren_on` | `device`, `name` | 1 while the siren sounds |
| `levelsense_relay_on` | `device`, `name` | 1 while the relay is switched on |
| `levelsense_alarm_silenced` | `device`, `name` | 1 once the alarm has been silenced |
| `levelsense_device_state` | `device`, `name` | a state number, 0 in normal running; what else it means is not documented |
| `levelsense_config_waiting` | `device`, `name` | 1 when the cloud said new settings are waiting for the freezer |
| `levelsense_value` | `device`, `name`, `field`, `index` | every number in the last report, by its field and its place in a list |
| `levelsense_reports_total` | `device`, `name` | reports the freezer has sent |
| `levelsense_last_report_timestamp_seconds` | `device`, `name` | when it last reported |
| `levelsense_device_info` | `device`, `name`, `mac`, `address`, `firmware` | always 1, with its MAC address, IP address and the firmware it runs, such as `SENTRY_020122` |
| `levelsense_requests_total` | `path` | requests made: `/_device/data` reports, `/_device/config` settings fetches, and `other` |
| `levelsense_cloud_replies_total` | `status` | the cloud's answers by HTTP status, and `unreachable` for none |
| `levelsense_cloud_reply_seconds` | | how long the cloud took to give its last answer |
| `levelsense_cloud_last_reply_timestamp_seconds` | | when the cloud last answered |
| `levelsense_offline_mode` | | 1 while the freezer is answered here and the cloud is sent nothing |
| `levelsense_interceptor_build_info` | `version`, `commit`, `goversion` | always 1; the version running |

Where a field is a list of readings, the named reading is its first. Both community projects read the first as the current value, though they disagree about what the others are.

A few things to ask of them:

```promql
levelsense_temperature_celsius > -12                                        # the freezer is warming up
time() - levelsense_last_report_timestamp_seconds > 600                     # the freezer alarm has gone quiet
increase(levelsense_cloud_replies_total{status="unreachable"}[30m]) > 0     # the cloud could not be reached
rate(levelsense_requests_total{path="/_device/data"}[15m]) == 0             # no reports at all: is the DNS override in place?
```

## A raw copy

`LEVELSENSE_INTERCEPTOR_RAW_FILE` keeps every request and the cloud's answer as one JSON line each, until the file reaches `LEVELSENSE_INTERCEPTOR_RAW_MAX_MB`. It is there to see what the freezer really sends, and to test against.

- It holds the freezer's MAC address and ID. Only its owner can read it. Never commit it.
- In a container, the folder it is written to must be writable by uid 65532.
- It is added to, not replaced, when the interceptor restarts. It stops at its size, at the end of a line.

## How this is built

- **The parts are tested alone.** Reading a report however it is escaped, passing a request on without changing a character of it, handing back the cloud's answer byte for byte, the cloud being down, the readings page and the state for Home Assistant.
- **`serve` is tested end to end**, with a made-up cloud: a report goes in, reaches the cloud as sent, and comes out on the page, at `/state` and in the raw copy.
- **What no test reaches.** A real freezer alarm and the real cloud. The test reports are made up, shaped like the Level Sense Pro's. A raw copy of a real one is the way to check more.
- **Checks CI runs.** Unit tests under the race detector, golangci-lint, CodeQL, govulncheck and a scan of the built program. Linters for the workflows, shell scripts, YAML, markdown and spelling.

## More

- [Development and tests](docs/development.md)
- [Contributing and releases](CONTRIBUTING.md)
