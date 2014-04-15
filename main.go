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
	"github.com/cloudfoundry-incubator/stager/metrics"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
)

var index = flag.Uint(
	"index",
	0,
	"index for this stager (starting at 0)",
)

var metricsPort = flag.Uint(
	"metricsPort",
	5678,
	"the port for the /metricz endpoint",
)

var metricsUsername = flag.String(
	"metricsUsername",
	"",
	"basic auth username for the /metricz endpoint",
)

var metricsPassword = flag.String(
	"metricsPassword",
	"",
	"basic auth password for the /metricz endpoint",
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

func main() {
	flag.Parse()

	steno.Init(&steno.Config{
		Sinks: []steno.Sink{
			steno.NewIOSink(os.Stdout),
		},
	})

	log := steno.NewLogger("Stager")

	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdCluster, ","),
		workerpool.NewWorkerPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		log.Fatalf("Error connecting to etcd: %s\n", err)
	}

	natsClient := yagnats.NewClient()

	natsMembers := []yagnats.ConnectionProvider{}

	for _, addr := range strings.Split(*natsAddresses, ",") {
		natsMembers = append(
			natsMembers,
			&yagnats.ConnectionInfo{addr, *natsUsername, *natsPassword},
		)
	}

	err = natsClient.Connect(&yagnats.ConnectionCluster{
		Members: natsMembers,
	})

	if err != nil {
		log.Fatalf("Error connecting to NATS: %s\n", err)
	}

	bbs := Bbs.New(etcdAdapter, timeprovider.NewTimeProvider())

	compilersMap := make(map[string]string)
	err = json.Unmarshal([]byte(*compilers), &compilersMap)
	if err != nil {
		log.Fatalf("Error parsing compilers flag: %s\n", err)
	}

	go outbox.Listen(bbs, natsClient, log)
	go metrics.Listen(natsClient, log, metrics.Config{
		Index:      *index,
		StatusPort: uint32(*metricsPort),
		Username:   *metricsUsername,
		Password:   *metricsPassword,
	})

	inbox.Listen(natsClient, stager.New(bbs, compilersMap), inbox.ValidateRequest, log)

	fmt.Println("Listening for staging requests!")

	select {}
}
