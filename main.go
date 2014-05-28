package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/storeadapter/etcdstoreadapter"
	"github.com/cloudfoundry/storeadapter/workerpool"
	"github.com/cloudfoundry/yagnats"

	"github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
)

var etcdCluster = flag.String(
	"etcdCluster",
	"http://127.0.0.1:4001",
	"comma-separated list of etcd addresses (http://ip:port)",
)

var natsAddresses = flag.String(
	"natsAddresses",
	"127.0.0.1:4222",
	"comma-separated list of NATS addresses (ip:port)",
)

var natsUsername = flag.String(
	"natsUsername",
	"nats",
	"Username to connect to nats",
)

var natsPassword = flag.String(
	"natsPassword",
	"nats",
	"Password for nats user",
)

var compilers = flag.String(
	"compilers",
	"{}",
	"Map of compilers for different stacks (name => compiler_name)",
)

var syslogName = flag.String(
	"syslogName",
	"",
	"syslog program name",
)

func main() {
	flag.Parse()

	logger := initializeLogger()
	natsClient := yagnats.NewClient()

	go outbox.Listen(bbs, natsClient, logger)

	inbox.Listen(natsClient, stager.New(bbs, compilersMap), inbox.ValidateRequest, logger)

	fmt.Println("Listening for staging requests!")

	select {}
}

func initializeLogger() *steno.Logger {
	stenoConfig := &steno.Config{
		Sinks: []steno.Sink{
			steno.NewIOSink(os.Stdout),
		},
	}

	if *syslogName != "" {
		stenoConfig.Sinks = append(stenoConfig.Sinks, steno.NewSyslogSink(*syslogName))
	}

	steno.Init(stenoConfig)

	return steno.NewLogger("Stager")
}

func initializeCompilers(logger *steno.Logger) map[string]string {
	compilersMap := make(map[string]string)
	err = json.Unmarshal([]byte(*compilers), &compilersMap)
	if err != nil {
		logger.Fatalf("Error parsing compilers flag: %s\n", err)
	}
}

func initializeNatsClient(logger *steno.Logger) *yagnats.Client {
	natsClient := yagnats.NewClient()

	natsMembers := []yagnats.ConnectionProvider{}

	for _, addr := range strings.Split(*natsAddresses, ",") {
		natsMembers = append(
			natsMembers,
			&yagnats.ConnectionInfo{addr, *natsUsername, *natsPassword},
		)
	}

	err := natsClient.Connect(&yagnats.ConnectionCluster{
		Members: natsMembers,
	})

	if err != nil {
		logger.Fatalf("Error connecting to NATS: %s\n", err)
	}

	return natsClient
}

func initializeStagerBBS(logger *steno.Logger) bbs.StagerBBS {
	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdCluster, ","),
		workerpool.NewWorkerPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		logger.Fatalf("Error connecting to etcd: %s\n", err)
	}

	return Bbs.NewStagerBBS(etcdAdapter, timeprovider.NewTimeProvider(), logger)
}
