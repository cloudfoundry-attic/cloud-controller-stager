package main

import (
	"flag"
	"fmt"
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	stgr "github.com/cloudfoundry-incubator/stager/stager"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/storeadapter"
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

var natsAddress = flag.String(
	"natsAddress",
	"127.0.0.1:4222",
	"Address of nats server (ip:port)",
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

	etcdAdapter := storeadapter.NewETCDStoreAdapter(
		strings.Split(*etcdMachines, ","),
		workerpool.NewWorkerPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		log.Fatalf("Error connecting to etcd: %s\n", err)
	}

	natsClient := yagnats.NewClient()
	err = natsClient.Connect(&yagnats.ConnectionInfo{*natsAddress, *natsUsername, *natsPassword})
	if err != nil {
		log.Fatalf("Error connecting: %s\n", err)
	}

	stager := stgr.NewStager(Bbs.New(etcdAdapter))
	stgr.Listen(natsClient, stager, log)
	fmt.Println("Listening for staging requests!")

	select {}
}
