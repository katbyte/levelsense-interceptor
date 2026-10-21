# Contributing

Issues and pull requests are welcome.

- `make check-all` runs everything CI runs. See [development](docs/development.md).
- A report read wrongly is easiest to fix with the report itself: `LEVELSENSE_INTERCEPTOR_RAW_FILE` keeps it.
- Take the MAC address and the device ID out of a report before attaching it.

## Releases

- Changes collect under "Unreleased" in the [changelog](CHANGELOG.md).
- A version is tagged when a batch is worth upgrading for.
- Breaking changes are grouped into one release, not shipped one at a time.
