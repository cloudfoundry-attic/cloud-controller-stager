package main

import (
	"encoding/json"
	"flag"
	"net"
	"net/url"
	"os"

	"github.com/cloudfoundry/dropsonde"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	cf_lager "github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
)

var natsAddresses = flag.String(
	"natsAddresses",
	"",
	"comma-separated list of NATS addresses (ip:port)",
)

var natsUsername = flag.String(
	"natsUsername",
	"",
	"Username to connect to nats",
)

var natsPassword = flag.String(
	"natsPassword",
	"",
	"Password for nats user",
)

var ccBaseURL = flag.String(
	"ccBaseURL",
	"",
	"URI to acccess the Cloud Controller",
)

var ccUsername = flag.String(
	"ccUsername",
	"",
	"Basic auth username for CC internal API",
)

var ccPassword = flag.String(
	"ccPassword",
	"",
	"Basic auth password for CC internal API",
)

var skipCertVerify = flag.Bool(
	"skipCertVerify",
	false,
	"skip SSL certificate verification",
)

var lifecycles = flag.String(
	"lifecycles",
	"{}",
	"Map of lifecycles for different stacks (name => compiler_name)",
)

var dockerLifecyclePath = flag.String(
	"dockerLifecyclePath",
	"",
	"path for downloading docker lifecycle from file server",
)

var diegoAPIURL = flag.String(
	"diegoAPIURL",
	"",
	"URL of diego API",
)

var stagerURL = flag.String(
	"stagerURL",
	"",
	"URL of the stager",
)

var fileServerURL = flag.String(
	"fileServerURL",
	"",
	"URL of the file server",
)

const (
	dropsondeDestination = "localhost:3457"
	dropsondeOrigin      = "stager"
)

func main() {
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	logger := cf_lager.New("stager")
	initializeDropsonde(logger)

	ccClient := cc_client.NewCcClient(*ccBaseURL, *ccUsername, *ccPassword, *skipCertVerify)
	diegoAPIClient := receptor.NewClient(*diegoAPIURL)

	natsClient := diegonats.NewClient()

	address, err := getStagerAddress()
	if err != nil {
		logger.Fatal("Invalid stager URL", err)
	}

	natsRunner := diegonats.NewClientRunner(*natsAddresses, *natsUsername, *natsPassword, logger, natsClient)

	members := grouper.Members{
		{"nats", natsRunner},
	}

	backends := initializeBackends(logger)
	for _, backend := range backends {
		backend := backend
		members = append(members, grouper.Member{
			"inbox-" + backend.TaskDomain(), inbox.New(natsClient, ccClient, diegoAPIClient, backend, logger),
		})
	}

	members = append(members, grouper.Member{
		"outbox", outbox.New(address, ccClient, backends, logger, clock.NewClock()),
	})

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	process := ifrit.Invoke(sigmon.New(group))

	logger.Info("Listening for staging requests!")

	err = <-process.Wait()
	if err != nil {
		logger.Fatal("Stager exited with error", err)
	}
}

func initializeDropsonde(logger lager.Logger) {
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeBackends(logger lager.Logger) []backend.Backend {
	lifecyclesMap := make(map[string]string)
	err := json.Unmarshal([]byte(*lifecycles), &lifecyclesMap)
	if err != nil {
		logger.Fatal("Error parsing lifecycles flag", err)
	}
	config := backend.Config{
		CallbackURL:         *stagerURL,
		FileServerURL:       *fileServerURL,
		Lifecycles:          lifecyclesMap,
		DockerLifecyclePath: *dockerLifecyclePath,
		SkipCertVerify:      *skipCertVerify,
		Sanitizer:           cc_messages.SanitizeErrorMessage,
	}

	return []backend.Backend{
		backend.NewTraditionalBackend(config, logger),
		backend.NewDockerBackend(config, logger),
	}
}

func getStagerAddress() (string, error) {
	url, err := url.Parse(*stagerURL)
	if err != nil {
		return "", err
	}

	_, port, err := net.SplitHostPort(url.Host)
	if err != nil {
		return "", err
	}

	return "0.0.0.0:" + port, nil
}
