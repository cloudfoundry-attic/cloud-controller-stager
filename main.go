package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/storeadapter/etcdstoreadapter"
	"github.com/cloudfoundry/storeadapter/workerpool"
	"github.com/cloudfoundry/yagnats"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/sigmon"

	"github.com/cloudfoundry-incubator/cf-debug-server"
	"github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
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

var circuses = flag.String(
	"circuses",
	"{}",
	"Map of circuses for different stacks (name => compiler_name)",
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

func main() {
	flag.Parse()

	logger := cf_lager.New("stager")
	natsClient := initializeNatsClient(logger)
	stagerBBS := initializeStagerBBS(logger)
	stager := initializeStager(stagerBBS, logger)

	cf_debug_server.Run()

	group := ifrit.Envoke(grouper.RunGroup{
		"inbox":  inbox.New(natsClient, stager, inbox.ValidateRequest, logger),
		"outbox": outbox.New(stagerBBS, natsClient, logger),
	})

	monitor := ifrit.Envoke(sigmon.New(group))
	fmt.Println("Listening for staging requests!")

	err := <-monitor.Wait()
	if err != nil {
		logger.Fatal("Stager exited with error: %s", err)
	}
}

func initializeStager(stagerBBS bbs.StagerBBS, logger lager.Logger) stager.Stager {
	circusesMap := make(map[string]string)
	err := json.Unmarshal([]byte(*circuses), &circusesMap)
	if err != nil {
		logger.Fatal("Error parsing circuses flag: %s\n", err)
	}

	return stager.New(
		stagerBBS,
		logger,
		stager.Config{
			Circuses:           circusesMap,
			MinMemoryMB:        *minMemoryMB,
			MinDiskMB:          *minDiskMB,
			MinFileDescriptors: *minFileDescriptors,
		})
}

func initializeNatsClient(logger lager.Logger) *yagnats.Client {
	natsClient := yagnats.NewClient()

	natsMembers := []yagnats.ConnectionProvider{}

	for _, addr := range strings.Split(*natsAddresses, ",") {
		natsMembers = append(
			natsMembers,
			&yagnats.ConnectionInfo{
				Addr:     addr,
				Username: *natsUsername,
				Password: *natsPassword,
			},
		)
	}

	err := natsClient.Connect(&yagnats.ConnectionCluster{
		Members: natsMembers,
	})

	if err != nil {
		logger.Fatal("connecting-to-nats-failed", err)
	}

	return natsClient
}

func initializeStagerBBS(logger lager.Logger) bbs.StagerBBS {
	etcdAdapter := etcdstoreadapter.NewETCDStoreAdapter(
		strings.Split(*etcdCluster, ","),
		workerpool.NewWorkerPool(10),
	)

	err := etcdAdapter.Connect()
	if err != nil {
		logger.Fatal("failed-to-connect-to-etcd", err)
	}

	return bbs.NewStagerBBS(etcdAdapter, timeprovider.NewTimeProvider(), logger)
}
