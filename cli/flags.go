package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/levelsense-interceptor/lib/cloud"
	"github.com/katbyte/levelsense-interceptor/lib/rawcopy"
	"github.com/katbyte/levelsense-interceptor/lib/readings"
	"github.com/katbyte/levelsense-interceptor/lib/state"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// toolName is what the program is called.
const toolName = "levelsense-interceptor"

// The defaults. The freezer only ever sends to port 80, so that is where its requests are taken, and the readings are served on 9125, beside the
// other tools' 9123, 9124 and 9088. The cloud is looked up through public DNS, which knows nothing of the local override that points it here.
const (
	defaultListen        = ":80"
	defaultMetricsListen = ":9125"
	defaultRawMaxMB      = 100

	// systemDNS is the --cloud-dns that means the machine's own DNS servers.
	systemDNS = "system"
)

type FlagData struct {
	Listen        string        `mapstructure:"listen"`
	MetricsListen string        `mapstructure:"metrics-listen"`
	Cloud         string        `mapstructure:"cloud"`
	CloudDNS      string        `mapstructure:"cloud-dns"`
	StaleAfter    time.Duration `mapstructure:"stale-after"`
	RawFile       string        `mapstructure:"raw-file"`
	RawMaxMB      int           `mapstructure:"raw-max-mb"`
	Labels        string        `mapstructure:"labels"`
	Names         string        `mapstructure:"names"`
	Offline       bool          `mapstructure:"offline"`
}

// envNames binds each flag to its environment variable.
var envNames = map[string]string{
	"listen":         "LEVELSENSE_INTERCEPTOR_LISTEN",
	"metrics-listen": "LEVELSENSE_INTERCEPTOR_METRICS_LISTEN",
	"cloud":          "LEVELSENSE_INTERCEPTOR_CLOUD",
	"cloud-dns":      "LEVELSENSE_INTERCEPTOR_CLOUD_DNS",
	"stale-after":    "LEVELSENSE_INTERCEPTOR_STALE_AFTER",
	"raw-file":       "LEVELSENSE_INTERCEPTOR_RAW_FILE",
	"raw-max-mb":     "LEVELSENSE_INTERCEPTOR_RAW_MAX_MB",
	"labels":         "LEVELSENSE_INTERCEPTOR_LABELS",
	"names":          "LEVELSENSE_INTERCEPTOR_NAMES",
	"offline":        "LEVELSENSE_INTERCEPTOR_OFFLINE",
}

// persistent is the flag set configureFlags bound, for the one setting that has to be read before viper is: where the settings file is.
var persistent *pflag.FlagSet

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.String("listen", defaultListen, "take the freezer's requests on this address; it only ever sends to port 80")
	pflags.String("metrics-listen", defaultMetricsListen, "serve the readings at /metrics, the state for Home Assistant at /state, and a health check, on this address")
	pflags.String("cloud", cloud.DefaultHost, "the Level Sense cloud the requests are passed on to: a name or an address, with a port when it is not 80")
	pflags.String("cloud-dns", strings.Join(cloud.DefaultDNS, ","), "the DNS servers the cloud is looked up through, tried in turn, or "+systemDNS+" for the machine's own; they must not be the ones that point the cloud's name here")
	pflags.Duration("stale-after", state.DefaultStaleAfter, "how long a freezer alarm can go unheard before Home Assistant is told it is offline")
	pflags.String("raw-file", "", "also keep every request and the cloud's answer, as they were, in this file: to see what the freezer really sends. They hold its MAC address and ID")
	pflags.Int("raw-max-mb", defaultRawMaxMB, "stop adding to --raw-file once it holds this many megabytes")
	pflags.String("labels", "", "labels to put on every reading, written name=value,name=value, e.g. hostname=dock")
	pflags.String("names", "", "names for the freezer alarms, written id=name,id=name, e.g. LS-0042=garage-freezer: each is served at /state/{name} as well as /state/{id}, and its readings carry both")
	pflags.Bool("offline", false, "answer the freezer here, as the cloud would, and send the cloud nothing: the app and its alerts stop, and the cloud sees the freezer go offline")
	pflags.String("config", "", "read settings from this file and no other, in place of ./.levelsense-interceptor or ~/.levelsense-interceptor (or export LEVELSENSE_INTERCEPTOR_CONFIG)")

	persistent = pflags

	for name, env := range envNames {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if err := viper.BindEnv(name, env); err != nil {
			return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
		}
	}

	return nil
}

// configEnv names the settings file from the environment, as --config does on the command line.
const configEnv = "LEVELSENSE_INTERCEPTOR_CONFIG"

// configPath is the settings file a run was told to read, "" for none: the flag's, then the environment's. A leading ~/ is the home directory, for
// whatever starts the program without a shell to expand it.
func configPath() string {
	path := os.Getenv(configEnv)
	if persistent != nil {
		if fl := persistent.Lookup("config"); fl != nil && fl.Changed {
			path = fl.Value.String()
		}
	}

	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}

	return path
}

// loadConfig reads the settings file, once the flags are parsed: the one --config or LEVELSENSE_INTERCEPTOR_CONFIG names and no other, or else the
// first .levelsense-interceptor found in the working directory and then in $HOME. A file that was named and cannot be read is an error; one merely
// absent from the usual places is not.
func loadConfig() error {
	viper.SetConfigType("env")
	if path := configPath(); path != "" {
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("reading the settings file %s (--config / %s): %w", path, configEnv, err)
		}
	} else {
		viper.SetConfigName(".levelsense-interceptor")
		// viper reads the first file it finds, so the working directory comes first
		viper.AddConfigPath(".")
		if home, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(home)
		}

		if err := viper.ReadInConfig(); err != nil {
			if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
				clog.Log.Errorf("Error reading config file: %v", err)
			}
		}
	}

	// a settings file spells a setting the way the environment does, less the prefix (CLOUD_DNS for --cloud-dns), and viper only matches a key spelled
	// as the flag is. Carried across as defaults, they still lose to a flag or the environment.
	for name := range envNames {
		if alt := strings.ReplaceAll(name, "-", "_"); alt != name && viper.InConfig(alt) {
			viper.SetDefault(name, viper.Get(alt))
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData. It is read from viper rather than from the flags themselves, since viper is what lays the
// environment and the settings file under the command line.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// upstream is the Level Sense cloud the settings say requests are passed on to.
func (f *FlagData) upstream() (*cloud.Cloud, error) {
	var servers []string
	if dns := strings.TrimSpace(f.CloudDNS); dns != systemDNS {
		for server := range strings.SplitSeq(dns, ",") {
			if server = strings.TrimSpace(server); server != "" {
				servers = append(servers, server)
			}
		}

		if len(servers) == 0 {
			return nil, fmt.Errorf("--cloud-dns (%s) names no DNS server: give one, or %s for the machine's own", envNames["cloud-dns"], systemDNS)
		}
	}

	c, err := cloud.New(cloud.Options{Host: strings.TrimSpace(f.Cloud), DNS: servers})
	if err != nil {
		return nil, fmt.Errorf("--cloud (%s): %w", envNames["cloud"], err)
	}

	return c, nil
}

// raw is the raw copy the settings ask for, nil when they ask for none.
func (f *FlagData) raw() (*rawcopy.File, error) {
	if f.RawFile == "" {
		return nil, nil //nolint:nilnil // no file is asked for, which is no fault
	}
	if f.RawMaxMB < 1 {
		return nil, fmt.Errorf("--raw-max-mb (%s) has to be at least 1", envNames["raw-max-mb"])
	}

	c, err := rawcopy.Open(f.RawFile, int64(f.RawMaxMB)*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("--raw-file (%s): %w", envNames["raw-file"], err)
	}

	return c, nil
}

// names are the names the settings give the freezer alarms, by ID. A name is part of an address, /state/{name}, so it cannot hold a /, and no two
// freezers can share one.
func (f *FlagData) names() (map[string]string, error) {
	names, ids := map[string]string{}, map[string]string{}
	for pair := range strings.SplitSeq(f.Names, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}

		id, name, ok := strings.Cut(pair, "=")
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		switch {
		case !ok || id == "" || name == "":
			return nil, fmt.Errorf("--names (%s): %q is not written id=name", envNames["names"], pair)
		case strings.Contains(name, "/"):
			return nil, fmt.Errorf("--names (%s): %q holds a /, and a name is part of the address /state/{name}", envNames["names"], name)
		case names[id] != "":
			return nil, fmt.Errorf("--names (%s): %s is named twice", envNames["names"], id)
		case ids[name] != "":
			return nil, fmt.Errorf("--names (%s): %s and %s are both named %s", envNames["names"], ids[name], id, name)
		}

		names[id], ids[name] = name, id
	}

	return names, nil
}

// page is the page of the readings, with the labels the settings ask for on every one.
func (f *FlagData) page(source readings.Source) (*metrics.Page, error) {
	labels, err := metrics.ParseLabels(f.Labels)
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	p, err := metrics.New(metrics.Options{Name: toolName, Labels: labels}, readings.New(source))
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	return p, nil
}
