package handlers

import (
	"net/http"

	"github.com/cloudfoundry-incubator/bbs"
	"github.com/cloudfoundry-incubator/stager"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/pivotal-golang/clock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
)

func New(logger lager.Logger, ccClient cc_client.CcClient, bbsClient bbs.Client, backends map[string]backend.Backend, clock clock.Clock) http.Handler {

	stagingHandler := NewStagingHandler(logger, backends, bbsClient)
	stagingCompletedHandler := NewStagingCompletionHandler(logger, ccClient, backends, clock)

	actions := rata.Handlers{
		stager.StageRoute:            http.HandlerFunc(stagingHandler.Stage),
		stager.StopStagingRoute:      http.HandlerFunc(stagingHandler.StopStaging),
		stager.StagingCompletedRoute: http.HandlerFunc(stagingCompletedHandler.StagingComplete),
	}

	handler, err := rata.NewRouter(stager.Routes, actions)
	if err != nil {
		panic("unable to create router: " + err.Error())
	}

	return handler
}
