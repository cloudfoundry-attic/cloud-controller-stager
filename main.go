package main

import (
	"flag"
	"fmt"
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/stager/outbox"
	stgr "github.com/cloudfoundry-incubator/stager/stager"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/storeadapter/etcdstoreadapter"
	"github.com/cloudfoundry/storeadapter/workerpool"
	"github.com/cloudfoundry/yagnats"
	"os"
	"strings"
)

var etcdMachines = flag.String(
	"etcdMachines",
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

func main() {
	flag.Parse()

	steno.Init(&steno.Config{
		Sinks: []steno.Sink{
			steno.NewIOSink(os.Stdout),
		},
	})

	log := steno.NewLogger("Stager")

	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdMachines, ","),
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

	bbs := Bbs.New(etcdAdapter)

	stager := stgr.NewStager(bbs)

	err = stgr.Listen(natsClient, stager, log)
	if err != nil {
		log.Fatalf("Could not subscribe on NATS: %s\n", err)
	}

	go outbox.Listen(bbs, natsClient, log)

	fmt.Println("Listening for staging requests!")

	select {}
}
