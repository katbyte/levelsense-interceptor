// Package main implements levelsense-interceptor, which sits between a Level Sense freezer alarm and the Level Sense cloud, passes everything on
// unchanged, and reads the freezer's reports on the way through for Prometheus and Home Assistant.
package main

import (
	"os"

	"github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/levelsense-interceptor/cli"
)

func main() {
	// the log level comes from LEVELSENSE_INTERCEPTOR_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("LEVELSENSE_INTERCEPTOR_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(color.Sprintf("<red>levelsense-interceptor: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(color.Sprintf("<red>levelsense-interceptor:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
