package metrics

import (
	"github.com/cloudfoundry-incubator/metricz"
	"github.com/cloudfoundry-incubator/metricz/collector_registrar"
	"github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

type Config struct {
	Index      uint
	StatusPort uint32
	Username   string
	Password   string
}

func Listen(natsClient yagnats.NATSClient, logger *gosteno.Logger, config Config) error {
	component, err := metricz.NewComponent(
		logger,
		"Stager",
		config.Index,
		metricz.NewDummyHealthMonitor(),
		config.StatusPort,
		[]string{config.Username, config.Password},
		nil,
	)

	if err != nil {
		return err
	}

	registrar := collector_registrar.New(natsClient)
	err = registrar.RegisterWithCollector(component)
	if err != nil {
		return err
	}

	return component.StartMonitoringEndpoints()
}
