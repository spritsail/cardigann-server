package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	_ "net/http/pprof"

	"github.com/Sirupsen/logrus"
	"github.com/cardigann/cardigann/config"
	"github.com/cardigann/cardigann/indexer"
	"github.com/cardigann/cardigann/logger"
	"github.com/cardigann/cardigann/server"
	"github.com/cardigann/cardigann/torznab"
	"github.com/equinox-io/equinox"
	"github.com/kardianos/service"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	Version string
	log     = logger.Logger
)

func version() string {
	if Version == "" {
		return "dev"
	}
	return Version
}

func main() {
	run(os.Args[1:], os.Exit)
}

func run(args []string, exit func(code int)) {
	app := kingpin.New("cardigann",
		`A torznab proxy for torrent indexer sites`)

	app.Version(version())
	app.Writer(os.Stdout)
	app.DefaultEnvars()

	app.Terminate(exit)

	if err := configureServerCommand(app); err != nil {
		log.Error(err)
		return
	}

	configureQueryCommand(app)
	configureDownloadCommand(app)
	configureTestDefinitionCommand(app)
	configureServiceCommand(app)
	configureUpdateCommand(app)
	configureRatiosCommand(app)

	kingpin.MustParse(app.Parse(args))
}

func newConfig() (config.Config, error) {
	f, err := config.GetConfigPath()
	if err != nil {
		return nil, err
	}

	log.WithField("path", f).Debug("Reading config")
	return config.NewJSONConfig(f)
}

func lookupRunner(key string, opts indexer.RunnerOpts) (torznab.Indexer, error) {
	if key == "aggregate" {
		return lookupAggregate(opts)
	}

	def, err := indexer.DefaultDefinitionLoader.Load(key)
	if err != nil {
		return nil, err
	}

	return indexer.NewRunner(def, opts), nil
}

func lookupAggregate(opts indexer.RunnerOpts) (torznab.Indexer, error) {
	keys, err := indexer.DefaultDefinitionLoader.List()
	if err != nil {
		return nil, err
	}

	agg := indexer.Aggregate{}
	for _, key := range keys {
		if config.IsSectionEnabled(key, opts.Config) {
			def, err := indexer.DefaultDefinitionLoader.Load(key)
			if err != nil {
				return nil, err
			}

			agg = append(agg, indexer.NewRunner(def, opts))
		}
	}

	return agg, nil
}

var globals struct {
	Debug bool
}

func configureGlobalFlags(cmd *kingpin.CmdClause) {
	cmd.Flag("debug", "Print out debug logging").BoolVar(&globals.Debug)
}

func applyGlobalFlags() {
	if globals.Debug {
		logger.SetLevel(logrus.DebugLevel)
	}
}

func configureQueryCommand(app *kingpin.Application) {
	var key, format string
	var args []string

	cmd := app.Command("query", "Manually query an indexer using torznab commands")
	cmd.Alias("q")
	cmd.Flag("format", "Either json, xml or rss").
		Default("json").
		Short('f').
		EnumVar(&format, "xml", "json", "rss")

	cmd.Arg("key", "The indexer key").
		Required().
		StringVar(&key)

	cmd.Arg("args", "Arguments to use to query").
		StringsVar(&args)

	configureGlobalFlags(cmd)

	cmd.Action(func(c *kingpin.ParseContext) error {
		applyGlobalFlags()
		return queryCommand(key, format, args)
	})
}

func queryCommand(key, format string, args []string) error {
	conf, err := newConfig()
	if err != nil {
		return err
	}

	indexer, err := lookupRunner(key, indexer.RunnerOpts{
		Config: conf,
	})
	if err != nil {
		return err
	}

	vals := url.Values{}
	for _, arg := range args {
		tokens := strings.SplitN(arg, "=", 2)
		if len(tokens) == 1 {
			vals.Set("q", tokens[0])
		} else {
			vals.Add(tokens[0], tokens[1])
		}
	}

	query, err := torznab.ParseQuery(vals)
	if err != nil {
		return fmt.Errorf("Parsing query failed: %s", err.Error())
	}

	feed, err := indexer.Search(query)
	if err != nil {
		return fmt.Errorf("Searching failed: %s", err.Error())
	}

	switch format {
	case "xml":
		x, err := xml.MarshalIndent(feed, "", "  ")
		if err != nil {
			return fmt.Errorf("Failed to marshal XML: %s", err.Error())
		}
		fmt.Printf("%s", x)

	case "json":
		j, err := json.MarshalIndent(feed, "", "  ")
		if err != nil {
			return fmt.Errorf("Failed to marshal JSON: %s", err.Error())
		}
		fmt.Printf("%s", j)
	}

	return nil
}

func configureDownloadCommand(app *kingpin.Application) {
	var key, url, file string

	cmd := app.Command("download", "Download a torrent from the tracker")
	cmd.Arg("key", "The indexer key").
		Required().
		StringVar(&key)

	cmd.Arg("url", "The url of the file to download").
		Required().
		StringVar(&url)

	cmd.Arg("file", "The filename to download to").
		Required().
		StringVar(&file)

	configureGlobalFlags(cmd)

	cmd.Action(func(c *kingpin.ParseContext) error {
		applyGlobalFlags()
		return downloadCommand(key, url, file)
	})
}

func downloadCommand(key, url, file string) error {
	conf, err := newConfig()
	if err != nil {
		return err
	}

	indexer, err := lookupRunner(key, indexer.RunnerOpts{
		Config: conf,
	})
	if err != nil {
		return err
	}

	rc, _, err := indexer.Download(url)
	if err != nil {
		return fmt.Errorf("Downloading failed: %s", err.Error())
	}

	defer rc.Close()

	f, err := os.Create(file)
	if err != nil {
		return fmt.Errorf("Creating file failed: %s", err.Error())
	}

	n, err := io.Copy(f, rc)
	if err != nil {
		return fmt.Errorf("Creating file failed: %s", err.Error())
	}

	log.WithFields(logrus.Fields{"bytes": n}).Info("Downloading file")
	return nil
}

func configureServerCommand(app *kingpin.Application) error {
	conf, err := newConfig()
	if err != nil {
		return err
	}

	s, err := server.New(conf, version())
	if err != nil {
		return err
	}

	cmd := app.Command("server", "Run the proxy (and web) server")
	cmd.Flag("port", "The port to listen on").
		StringVar(&s.Port)

	cmd.Flag("bind", "The address to bind to").
		StringVar(&s.Bind)

	cmd.Flag("prefix", "A path prefix for the server").
		StringVar(&s.PathPrefix)

	cmd.Flag("passphrase", "Require a passphrase to view web interface").
		Short('p').
		StringVar(&s.Passphrase)

	cmd.Flag("hostname", "The hostname to use for the links back to the server").
		StringVar(&s.Hostname)

	configureGlobalFlags(cmd)
	cmd.Action(func(c *kingpin.ParseContext) error {
		applyGlobalFlags()
		return serverCommand(s)
	})

	return nil
}

func serverCommand(s *server.Server) error {
	if globals.Debug {
		go func() {
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	}

	return s.Listen()
}

func configureTestDefinitionCommand(app *kingpin.Application) {
	var f *os.File
	var savePath, replayPath string
	var cachePages, verbose bool

	cmd := app.Command("test-definition", "Test a yaml indexer definition file")
	cmd.Alias("test")

	cmd.Flag("verbose", "Wheter to show info logger output").
		BoolVar(&verbose)

	cmd.Flag("cachepages", "Whether to store the output of browser actions for debugging").
		BoolVar(&cachePages)

	cmd.Flag("save", "Save all requests and responses to a har file").
		StringVar(&savePath)

	cmd.Flag("replay", "Replay all responses from a har file").
		StringVar(&replayPath)

	cmd.Arg("file", "The definition yaml file").
		FileVar(&f)

	configureGlobalFlags(cmd)
	cmd.Action(func(c *kingpin.ParseContext) error {
		if !verbose {
			logger.SetLevel(logrus.WarnLevel)
		}

		applyGlobalFlags()
		return testDefinitionCommand(f, cachePages, savePath, replayPath)
	})
}

func testDefinitionCommand(f *os.File, cachePages bool, savePath, replayPath string) error {
	logOutput := &bytes.Buffer{}
	logger.SetOutput(logOutput)
	defer func() {
		if logOutput.Len() > 0 {
			fmt.Printf("\nLogging output:\n")
			io.Copy(os.Stderr, logOutput)
			fmt.Println()
		}
	}()

	conf, err := newConfig()
	if err != nil {
		return err
	}

	defs := []*indexer.IndexerDefinition{}

	if f == nil {
		var err error
		defs, err = indexer.LoadEnabledDefinitions(conf)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		def, err := indexer.ParseDefinitionFile(f)
		if err != nil {
			return err
		}
		defs = append(defs, def)
	}

	fmt.Printf("→ Testing %d definition(s) (%s/%s/%s)\n",
		len(defs),
		version(),
		runtime.GOOS, runtime.GOARCH,
	)

	for _, def := range defs {
		runner := indexer.NewRunner(def, indexer.RunnerOpts{
			Config:     conf,
			CachePages: cachePages,
		})
		tester := indexer.Tester{Runner: runner, Opts: indexer.TesterOpts{
			Download: true,
		}}
		if err = tester.Test(); err != nil {
			return fmt.Errorf("One or more tests failed")
		}
	}
	return nil
}

func configureServiceCommand(app *kingpin.Application) {
	var action string
	var userService bool
	var possibleActions = append(service.ControlAction[:], "run")

	cmd := app.Command("service", "Control the cardigann service")

	cmd.Flag("user", "Whether to use a user service rather than a system one").
		BoolVar(&userService)

	cmd.Arg("action", "One of "+strings.Join(possibleActions, ", ")).
		Required().
		EnumVar(&action, possibleActions...)

	configureGlobalFlags(cmd)
	cmd.Action(func(c *kingpin.ParseContext) error {
		applyGlobalFlags()

		prg, err := newProgram(programOpts{
			UserService: userService,
		})
		if err != nil {
			return err
		}

		log.Debugf("Running service action %s on platform %v.", action, service.Platform())

		if action != "run" {
			return service.Control(prg.service, action)
		}

		return runServiceCommand(prg)
	})
}

func runServiceCommand(prg *program) error {
	var err error
	errs := make(chan error)
	prg.logger, err = prg.service.Logger(errs)
	if err != nil {
		log.Fatal(err)
	}

	logger.SetOutput(ioutil.Discard)
	logger.AddHook(&serviceLogHook{prg.logger})
	logger.SetFormatter(&serviceLogFormatter{})

	go func() {
		for {
			err := <-errs
			if err != nil {
				log.Error(err)
			}
		}
	}()

	err = prg.service.Run()
	if err != nil {
		prg.logger.Error(err)
	}

	return nil
}

func configureUpdateCommand(app *kingpin.Application) {
	var channel string
	var dryRun bool

	cmd := app.Command("update", "Update cardigann to the latest version")

	cmd.Flag("channel", "The channel to update from").
		EnumVar(&channel, "stable", "edge")

	cmd.Flag("dry-run", "Whether to do a dry run or to execute").
		BoolVar(&dryRun)

	configureGlobalFlags(cmd)
	cmd.Action(func(c *kingpin.ParseContext) error {
		return runUpdateCommand(channel, dryRun)
	})
}

const appID = "app_doJjayUsKxb"

var publicKey = []byte(`
-----BEGIN ECDSA PUBLIC KEY-----
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAEJmeGsHuOSDZI6nhtlWljGkwHVUow7yVx
KaGKMPQAGXIVEGg4kTYmDPTvCOoFmMZT+foLsJ2qu6xsLAavaZYlY7oXrYyNzM3S
x0cFjxMjM+8k+dQFEfYnemm5TUFQ3Hwz
-----END ECDSA PUBLIC KEY-----
`)

func runUpdateCommand(channel string, dryRun bool) error {
	opts := equinox.Options{
		Channel: channel,
	}

	if err := opts.SetPublicKeyPEM(publicKey); err != nil {
		return err
	}

	// check for the update
	resp, err := equinox.Check(appID, opts)
	switch {
	case err == equinox.NotAvailableErr:
		log.Info("No update available, already at the latest version!")
		return nil
	case err != nil:
		log.Errorf("Update failed:", err)
		return err
	}

	if dryRun {
		log.Infof("Update found from %s to %s, would be applied", Version, resp.ReleaseVersion)
		return nil
	}

	// fetch the update and apply it
	err = resp.Apply()
	if err != nil {
		return err
	}

	log.Infof("Updated to new version: %s!\n", resp.ReleaseVersion)
	return nil
}

func configureRatiosCommand(app *kingpin.Application) {
	cmd := app.Command("ratios", "Find your ratio on all your indexers")

	configureGlobalFlags(cmd)
	cmd.Action(func(c *kingpin.ParseContext) error {
		return runRatiosCommand()
	})
}

func runRatiosCommand() error {
	conf, err := newConfig()
	if err != nil {
		return err
	}

	defs, err := indexer.LoadEnabledDefinitions(conf)
	if err != nil {
		return err
	}

	for _, def := range defs {
		runner := indexer.NewRunner(def, indexer.RunnerOpts{
			Config: conf,
		})

		ratio, err := runner.Ratio()
		if err != nil {
			return fmt.Errorf("Failed to get ratio for %s: %v", def.Site, err)
		}

		fmt.Printf("Ratio for %s is %v\n", def.Site, ratio)
	}

	return nil
}
