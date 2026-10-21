# Changelog

## Unreleased

The first release: a program that sits between a Level Sense freezer alarm and the Level Sense cloud, and reads the freezer's reports on the way through.

- **The freezer's reports reach your graphs.** The temperature, humidity, battery voltage, Wi-Fi signal, siren, relay and silenced alarm are served as Prometheus readings, with every other number in the report beside them by field.
- **And Home Assistant.** Each freezer's state is served as JSON at `/state/{device}` for Home Assistant's REST integration, with whether it has reported recently.
- **Freezers can be given names.** A freezer is known by the ID it sends, a string of letters and digits; `LEVELSENSE_INTERCEPTOR_NAMES` gives it a name as well, for `/state/{name}` and as a label on its readings beside the ID.
- **The firmware each freezer runs** is on its `levelsense_device_info`, and the log says when it changes.
- **The app and its alerts go on working.** Every request is passed on to the real cloud unchanged, and the cloud's answer handed back as the cloud gave it.
- **The cloud is found without going in a circle.** It is looked up through public DNS, since the local DNS has been told to point its name here, and a name found only on a private network is refused.
- **An offline mode**, off unless it is set, that answers the freezer here as the cloud would and sends the cloud nothing. The freezer stays connected and its readings stay at home; the app's alerts stop.
- **What the cloud does is counted too**: its answers by status, the times it could not be reached, and how long it took.
- **A raw copy of every request and answer**, when asked for, to see what the freezer really sends. It stops at a size you choose and only its owner can read it.
- Ships as a Docker image and ready-built programs for Linux.
