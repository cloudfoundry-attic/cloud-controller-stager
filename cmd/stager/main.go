package main

import (
	"errors"
	"flag"
	"net"
	"net/url"
	"os"

	"github.com/cloudfoundry/dropsonde"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	cf_lager "github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages/flags"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/handlers"
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

var dockerStagingStack = flag.String(
	"dockerStagingStack",
	"",
	"Stack to use for staging Docker applications",
)

const (
	dropsondeDestination = "localhost:3457"
	dropsondeOrigin      = "stager"
)

func main() {
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)

	lifecycles := flags.LifecycleMap{}
	flag.Var(&lifecycles, "lifecycle", "app lifecycle binary bundle mapping (lifecycle[/stack]:bundle-filepath-in-fileserver)")
	flag.Parse()

	logger, reconfigurableSink := cf_lager.New("stager")
	initializeDropsonde(logger)

	ccClient := cc_client.NewCcClient(*ccBaseURL, *ccUsername, *ccPassword, *skipCertVerify)
	diegoAPIClient := receptor.NewClient(*diegoAPIURL)

	address, err := getStagerAddress()
	if err != nil {
		logger.Fatal("Invalid stager URL", err)
	}

	backends := initializeBackends(logger, lifecycles)

	handler := handlers.New(logger, ccClient, diegoAPIClient, backends, clock.NewClock())

	members := grouper.Members{
		{"server", http_server.New(address, handler)},
	}

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr, reconfigurableSink)},
		}, members...)
	}

	logger.Info("starting")

	group := grouper.NewOrdered(os.Interrupt, members)

	process := ifrit.Invoke(sigmon.New(group))

	logger.Info("Listening for staging requests!")

	err = <-process.Wait()
	if err != nil {
		logger.Fatal("Stager exited with error", err)
	}

	logger.Info("stopped")
}

func initializeDropsonde(logger lager.Logger) {
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeBackends(logger lager.Logger, lifecycles flags.LifecycleMap) map[string]backend.Backend {
	_, err := url.Parse(*stagerURL)
	if err != nil {
		logger.Fatal("Error parsing stager URL", err)
	}
	if *dockerStagingStack == "" {
		logger.Fatal("Invalid Docker staging stack", errors.New("dockerStagingStack cannot be blank"))
	}

	config := backend.Config{
		TaskDomain:         backend.StagingTaskDomain,
		StagerURL:          *stagerURL,
		FileServerURL:      *fileServerURL,
		Lifecycles:         lifecycles,
		SkipCertVerify:     *skipCertVerify,
		Sanitizer:          cc_messages.SanitizeErrorMessage,
		DockerStagingStack: *dockerStagingStack,
	}

	return map[string]backend.Backend{
		"buildpack": backend.NewTraditionalBackend(config, logger),
		"docker":    backend.NewDockerBackend(config, logger),
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
