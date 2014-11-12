package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/cloudfoundry/dropsonde"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/receptor"
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

var circuses = flag.String(
	"circuses",
	"{}",
	"Map of circuses for different stacks (name => compiler_name)",
)

var dockerCircusPath = flag.String(
	"dockerCircusPath",
	"",
	"path for downloading docker circus from file server",
)

var minMemoryMB = flag.Uint(
	"minMemoryMB",
	1024,
	"minimum memory limit for staging tasks",
)

var minDiskMB = flag.Uint(
	"minDiskMB",
	3072,
	"minimum disk limit for staging tasks",
)

var minFileDescriptors = flag.Uint64(
	"minFileDescriptors",
	0,
	"minimum file descriptors for staging tasks",
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

var dropsondeOrigin = flag.String(
	"dropsondeOrigin",
	"stager",
	"Origin identifier for dropsonde-emitted metrics.",
)

var dropsondeDestination = flag.String(
	"dropsondeDestination",
	"localhost:3457",
	"Destination for dropsonde-emitted metrics.",
)

func main() {
	flag.Parse()

	logger := cf_lager.New("stager")
	initializeDropsonde(logger)

	ccClient := cc_client.NewCcClient(*ccBaseURL, *ccUsername, *ccPassword, *skipCertVerify)
	diegoAPIClient := receptor.NewClient(*diegoAPIURL, "", "")

	cf_debug_server.Run()

	natsClient := diegonats.NewClient()

	address, err := getStagerAddress()
	if err != nil {
		logger.Fatal("Invalid stager URL", err)
	}

	var members grouper.Members
	members = append(members, grouper.Member{
		Name:   "nats",
		Runner: diegonats.NewClientRunner(*natsAddresses, *natsUsername, *natsPassword, logger, natsClient),
	})

	backends := initializeBackends(logger)
	for _, backend := range backends {
		backend := backend
		members = append(members, grouper.Member{
			Name: fmt.Sprintf("inbox-%s", backend.TaskDomain()),
			Runner: ifrit.RunFunc(
				func(signals <-chan os.Signal, ready chan<- struct{}) error {
					return inbox.New(natsClient, ccClient, diegoAPIClient, backend, logger).Run(signals, ready)
				},
			),
		})
	}

	members = append(members, grouper.Member{
		Name:   "outbox",
		Runner: outbox.New(address, ccClient, backends, logger, timeprovider.NewTimeProvider()),
	})

	group := grouper.NewOrdered(os.Interrupt, members)

	process := ifrit.Invoke(sigmon.New(group))

	logger.Info("Listening for staging requests!")

	err = <-process.Wait()
	if err != nil {
		logger.Fatal("Stager exited with error", err)
	}
}

func initializeDropsonde(logger lager.Logger) {
	err := dropsonde.Initialize(*dropsondeDestination, *dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeBackends(logger lager.Logger) []backend.Backend {
	circusesMap := make(map[string]string)
	err := json.Unmarshal([]byte(*circuses), &circusesMap)
	if err != nil {
		logger.Fatal("Error parsing circuses flag", err)
	}
	config := backend.Config{
		CallbackURL:        *stagerURL,
		FileServerURL:      *fileServerURL,
		Circuses:           circusesMap,
		DockerCircusPath:   *dockerCircusPath,
		MinMemoryMB:        *minMemoryMB,
		MinDiskMB:          *minDiskMB,
		MinFileDescriptors: *minFileDescriptors,
		SkipCertVerify:     *skipCertVerify,
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
