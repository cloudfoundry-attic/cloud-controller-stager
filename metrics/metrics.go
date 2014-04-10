package metrics

import (
	"github.com/cloudfoundry-incubator/metricz"
	"github.com/cloudfoundry-incubator/metricz/collector_registrar"
	"github.com/cloudfoundry-incubator/metricz/instrumentation"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

type Config struct {
	Port     uint32
	Username string
	Password string
	Index    uint
}

func Listen(natsClient yagnats.NATSClient, logger *gosteno.Logger, config Config) error {
	registrar := collector_registrar.New(natsClient)

	component, err := metricz.NewComponent(
		logger,
		"Stager",
		config.Index,
		NewHealthCheck(),
		config.Port,
		[]string{config.Username, config.Password},
		[]instrumentation.Instrumentable{},
	)

	err = registrar.RegisterWithCollector(component)
	if err != nil {
		return err
	}

	go component.StartMonitoringEndpoints()

	return nil
}
