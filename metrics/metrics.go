package metrics

import (
	"github.com/cloudfoundry-incubator/metricz"
	"github.com/cloudfoundry-incubator/metricz/collector_registrar"
	"github.com/cloudfoundry-incubator/metricz/instrumentation"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

type Config struct {
	Port     uint32
	Username string
	Password string
	Index    uint
}

type MetricsServer struct {
	natsClient yagnats.NATSClient
	bbs        bbs.MetricsBBS
	logger     *gosteno.Logger
	config     Config
	component  metricz.Component
}

func NewMetricsServer(
	natsClient yagnats.NATSClient,
	bbs bbs.MetricsBBS,
	logger *gosteno.Logger,
	config Config) *MetricsServer {
	return &MetricsServer{
		natsClient: natsClient,
		bbs:        bbs,
		logger:     logger,
		config:     config,
	}
}

func (server *MetricsServer) Listen() error {
	registrar := collector_registrar.New(server.natsClient)

	var err error
	server.component, err = metricz.NewComponent(
		server.logger,
		"Stager",
		server.config.Index,
		NewHealthCheck(),
		server.config.Port,
		[]string{server.config.Username, server.config.Password},
		[]instrumentation.Instrumentable{
			NewTaskInstrument(server.bbs),
		},
	)

	err = registrar.RegisterWithCollector(server.component)
	if err != nil {
		return err
	}

	go server.component.StartMonitoringEndpoints()

	return nil
}

func (server *MetricsServer) Stop() {
	server.component.StopMonitoringEndpoints()
}
