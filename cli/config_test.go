package cli

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/levelsense-interceptor/lib/intercept"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const settingsFile = ".levelsense-interceptor"

// alone cuts a test off from the settings of whoever runs it: viper as it starts, an empty home and working directory, and none of the program's
// variables set.
func alone(t *testing.T) (home, wd string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	home, wd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(wd)
	t.Setenv(configEnv, "")
	for _, env := range envNames {
		t.Setenv(env, "")
	}

	return home, wd
}

// load drives the real flag wiring with these command-line arguments, after whatever the test put in the environment and the settings files.
func load(t *testing.T, args ...string) (*FlagData, error) {
	t.Helper()

	root := &cobra.Command{Use: toolName}
	if err := configureFlags(root); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}
	if err := root.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := loadConfig(); err != nil {
		return nil, err
	}

	return GetFlags(), nil
}

// must is load for a test that expects the settings to be read.
func must(t *testing.T, args ...string) *FlagData {
	t.Helper()

	f, err := load(t, args...)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	return f
}

func write(t *testing.T, dir, file, body string) string {
	t.Helper()

	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// With nothing set, the freezer's requests are taken on port 80 and passed on to the real cloud, looked up through public DNS, and the readings are
// served where the README says.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestDefaults(t *testing.T) {
	alone(t)

	want := FlagData{
		Listen:        ":80",
		MetricsListen: ":9125",
		Cloud:         "cloud.level-sense.com",
		CloudDNS:      "1.1.1.1:53,8.8.8.8:53",
		StaleAfter:    15 * time.Minute,
		RawMaxMB:      100,
	}
	if got := *must(t); got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
}

// Every setting has an environment variable, which is how a container is configured.
func TestEveryFlagHasAnEnvironmentVariable(t *testing.T) {
	alone(t)

	t.Setenv("LEVELSENSE_INTERCEPTOR_LISTEN", ":8080")
	t.Setenv("LEVELSENSE_INTERCEPTOR_METRICS_LISTEN", "127.0.0.1:9000")
	t.Setenv("LEVELSENSE_INTERCEPTOR_CLOUD", "104.192.6.192")
	t.Setenv("LEVELSENSE_INTERCEPTOR_CLOUD_DNS", "9.9.9.9:53")
	t.Setenv("LEVELSENSE_INTERCEPTOR_STALE_AFTER", "15m")
	t.Setenv("LEVELSENSE_INTERCEPTOR_RAW_FILE", "/data/freezer.raw")
	t.Setenv("LEVELSENSE_INTERCEPTOR_RAW_MAX_MB", "5")
	t.Setenv("LEVELSENSE_INTERCEPTOR_LABELS", "hostname=dock")
	t.Setenv("LEVELSENSE_INTERCEPTOR_NAMES", "LS-0042=garage-freezer")
	t.Setenv("LEVELSENSE_INTERCEPTOR_OFFLINE", "true")

	want := FlagData{
		Listen:        ":8080",
		MetricsListen: "127.0.0.1:9000",
		Cloud:         "104.192.6.192",
		CloudDNS:      "9.9.9.9:53",
		StaleAfter:    15 * time.Minute,
		RawFile:       "/data/freezer.raw",
		RawMaxMB:      5,
		Labels:        "hostname=dock",
		Names:         "LS-0042=garage-freezer",
		Offline:       true,
	}
	if got := *must(t); got != want {
		t.Errorf("from the environment = %+v, want %+v", got, want)
	}

	root := &cobra.Command{Use: toolName}
	if err := configureFlags(root); err != nil {
		t.Fatal(err)
	}
	root.PersistentFlags().VisitAll(func(fl *pflag.Flag) {
		if _, ok := envNames[fl.Name]; !ok && fl.Name != "config" {
			t.Errorf("--%s has no environment variable", fl.Name)
		}
	})
}

// A flag beats the environment, which beats a settings file: a container overrides what an image holds, and a command line overrides both.
func TestFlagThenEnvironmentThenFile(t *testing.T) {
	home, _ := alone(t)
	write(t, home, settingsFile, "CLOUD=from-file\nCLOUD_DNS=system\nSTALE_AFTER=1h\nRAW_MAX_MB=7\n")

	f := must(t)
	if f.Cloud != "from-file" || f.CloudDNS != "system" || f.StaleAfter != time.Hour || f.RawMaxMB != 7 {
		t.Errorf("from the file = %+v, want every setting it holds, those of several words included", f)
	}

	viper.Reset()
	t.Setenv("LEVELSENSE_INTERCEPTOR_CLOUD", "from-env")
	t.Setenv("LEVELSENSE_INTERCEPTOR_RAW_MAX_MB", "8")
	f = must(t)
	if f.Cloud != "from-env" || f.RawMaxMB != 8 || f.CloudDNS != "system" {
		t.Errorf("with the environment = %+v, want it over the file, and the file for the rest", f)
	}

	viper.Reset()
	f = must(t, "--cloud", "from-flag", "--raw-max-mb", "9")
	if f.Cloud != "from-flag" || f.RawMaxMB != 9 {
		t.Errorf("with flags = %+v, want them over the environment", f)
	}
}

// A .levelsense-interceptor in the working directory is found before the one in $HOME, and neither being there is not an error.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestSettingsFilePrecedence(t *testing.T) {
	home, wd := alone(t)

	if f := must(t); f.Labels != "" {
		t.Errorf("labels = %q with no settings file, want none", f.Labels)
	}

	viper.Reset()
	write(t, home, settingsFile, "LABELS=from=home\n")
	if f := must(t); f.Labels != "from=home" {
		t.Errorf("labels = %q, want the ones in $HOME", f.Labels)
	}

	viper.Reset()
	write(t, wd, settingsFile, "LABELS=from=here\n")
	if f := must(t); f.Labels != "from=here" {
		t.Errorf("labels = %q, want the working directory's over $HOME's", f.Labels)
	}
}

// A settings file that is named is the only one read, and one that cannot be read is an error: a run told where its settings are must not come
// up with someone else's, or with none.
func TestANamedSettingsFile(t *testing.T) {
	home, _ := alone(t)
	write(t, home, settingsFile, "LABELS=from=home\n")
	named := write(t, home, "other.env", "LISTEN=:8080\n")

	f := must(t, "--config", named)
	if f.Listen != ":8080" || f.Labels != "" {
		t.Errorf("with --config = %+v, want the named file and nothing of $HOME's", f)
	}

	viper.Reset()
	t.Setenv(configEnv, "~/other.env")
	if f := must(t); f.Listen != ":8080" {
		t.Errorf("with %s=~/other.env the freezer's requests are taken on %q, want the named file's", configEnv, f.Listen)
	}

	viper.Reset()
	if _, err := load(t, "--config", filepath.Join(home, "missing.env")); err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Errorf("a named file that is not there = %v, want an error naming it", err)
	}
}

// Settings that cannot be used are said before anything is listened on, naming the setting.
func TestSettingsThatCannotBeUsed(t *testing.T) {
	t.Parallel()

	good := FlagData{Cloud: "cloud.level-sense.com", CloudDNS: "1.1.1.1:53", RawMaxMB: 1}

	tests := []struct {
		name  string
		edit  func(*FlagData)
		check func(*FlagData) error
		says  string
	}{
		{"no cloud", func(f *FlagData) { f.Cloud = " " }, upstreamErr, "LEVELSENSE_INTERCEPTOR_CLOUD"},
		{"a cloud written as an address on the web", func(f *FlagData) { f.Cloud = "http://cloud.level-sense.com" }, upstreamErr, "LEVELSENSE_INTERCEPTOR_CLOUD"},
		{"no DNS server", func(f *FlagData) { f.CloudDNS = " , " }, upstreamErr, "LEVELSENSE_INTERCEPTOR_CLOUD_DNS"},
		{"a raw copy of nothing", func(f *FlagData) { f.RawFile, f.RawMaxMB = "freezer.raw", 0 }, rawErr, "LEVELSENSE_INTERCEPTOR_RAW_MAX_MB"},
		{"a raw copy where it cannot go", func(f *FlagData) { f.RawFile = "/no/such/dir/freezer.raw" }, rawErr, "LEVELSENSE_INTERCEPTOR_RAW_FILE"},
		{"labels written wrongly", func(f *FlagData) { f.Labels = "hostname" }, pageErr, "LEVELSENSE_INTERCEPTOR_LABELS"},
		{"a label a reading has already", func(f *FlagData) { f.Labels = "device=freezer" }, pageErr, "LEVELSENSE_INTERCEPTOR_LABELS"},
		{"the label names are kept for", func(f *FlagData) { f.Labels = "name=freezer" }, pageErr, "LEVELSENSE_INTERCEPTOR_LABELS"},
		{"a name written wrongly", func(f *FlagData) { f.Names = "LS-0042" }, namesErr, "LEVELSENSE_INTERCEPTOR_NAMES"},
		{"a name left out", func(f *FlagData) { f.Names = "LS-0042= " }, namesErr, "LEVELSENSE_INTERCEPTOR_NAMES"},
		{"a name that cannot be in an address", func(f *FlagData) { f.Names = "LS-0042=garage/freezer" }, namesErr, "LEVELSENSE_INTERCEPTOR_NAMES"},
		{"a freezer named twice", func(f *FlagData) { f.Names = "LS-0042=garage,LS-0042=kitchen" }, namesErr, "LEVELSENSE_INTERCEPTOR_NAMES"},
		{"two freezers with one name", func(f *FlagData) { f.Names = "LS-0042=garage,LS-0043=garage" }, namesErr, "LEVELSENSE_INTERCEPTOR_NAMES"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := new(good)
			tc.edit(f)

			err := tc.check(f)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("got %v, want an error naming %s", err, tc.says)
			}
		})
	}

	for name, check := range map[string]func(*FlagData) error{"upstream": upstreamErr, "raw": rawErr, "page": pageErr, "names": namesErr} {
		if err := check(new(good)); err != nil {
			t.Errorf("%s of good settings: %v", name, err)
		}
	}

	// the machine's own DNS is asked for by name
	f := new(good)
	f.CloudDNS = " system "
	if err := upstreamErr(f); err != nil {
		t.Errorf("--cloud-dns system: %v", err)
	}
}

func upstreamErr(f *FlagData) error { _, err := f.upstream(); return err }
func rawErr(f *FlagData) error      { _, err := f.raw(); return err }
func pageErr(f *FlagData) error     { _, err := f.page(intercept.New(intercept.Options{})); return err }
func namesErr(f *FlagData) error    { _, err := f.names(); return err }

// Names are read by ID, with the spaces and empty pairs a hand-written list picks up left out.
func TestNames(t *testing.T) {
	t.Parallel()

	got, err := (&FlagData{Names: " LS-0042 = garage-freezer ,, LS-0043=chest freezer,"}).names()
	if err != nil {
		t.Fatal(err)
	}

	if want := map[string]string{"LS-0042": "garage-freezer", "LS-0043": "chest freezer"}; !maps.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
}
