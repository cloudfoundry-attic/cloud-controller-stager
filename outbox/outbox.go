package outbox

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/tedsuo/ifrit/http_server"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/pivotal-golang/lager"
)

const (
	// Metrics
	stagingSuccessCounter  = metric.Counter("StagingRequestsSucceeded")
	stagingSuccessDuration = metric.Duration("StagingRequestSucceededDuration")
	stagingFailureCounter  = metric.Counter("StagingRequestsFailed")
	stagingFailureDuration = metric.Duration("StagingRequestFailedDuration")
)

type Outbox struct {
	address      string
	ccClient     cc_client.CcClient
	backends     []backend.Backend
	logger       lager.Logger
	timeProvider timeprovider.TimeProvider
}

func New(address string, ccClient cc_client.CcClient, backends []backend.Backend, logger lager.Logger, timeProvider timeprovider.TimeProvider) *Outbox {
	outboxLogger := logger.Session("outbox")

	return &Outbox{
		address:      address,
		ccClient:     ccClient,
		backends:     backends,
		logger:       outboxLogger,
		timeProvider: timeProvider,
	}
}

func (o *Outbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	server := http_server.New(o.address, http.HandlerFunc(o.handleRequest))
	return server.Run(signals, ready)
}

func (o *Outbox) handleRequest(res http.ResponseWriter, req *http.Request) {
	var task receptor.TaskResponse
	err := json.NewDecoder(req.Body).Decode(&task)
	if err != nil {
		o.logger.Error("parsing-incoming-task-failed", err)
		res.WriteHeader(400)
		return
	}

	logger := o.logger.Session("task-complete-callback-received", lager.Data{
		"guid": task.TaskGuid,
	})

	responseJson, err := o.stagingResponse(task)
	if err != nil {
		res.WriteHeader(http.StatusBadRequest)
		logger.Error("get-staging-response-failed", err)
		return
	}

	if responseJson == nil {
		res.WriteHeader(404)
		res.Write([]byte("Unknown task domain"))
		return
	}

	logger.Info("posting-staging-complete", lager.Data{
		"payload": responseJson,
	})

	err = o.ccClient.StagingComplete(responseJson, logger)
	if err != nil {
		logger.Error("cc-request-failed", err)
		if responseErr, ok := err.(*cc_client.BadResponseError); ok {
			res.WriteHeader(responseErr.StatusCode)
		} else {
			res.WriteHeader(503)
		}
		return
	}

	o.reportMetrics(task)

	logger.Info("posted-staging-complete")
	res.WriteHeader(http.StatusOK)
}

func (o *Outbox) reportMetrics(task receptor.TaskResponse) {
	duration := o.timeProvider.Now().Sub(time.Unix(0, task.CreatedAt))
	if task.Failed {
		stagingFailureCounter.Increment()
		stagingFailureDuration.Send(duration)
	} else {
		stagingSuccessDuration.Send(duration)
		stagingSuccessCounter.Increment()
	}
}

func (o *Outbox) stagingResponse(task receptor.TaskResponse) ([]byte, error) {
	for _, backend := range o.backends {
		if backend.TaskDomain() == task.Domain {
			return backend.BuildStagingResponse(task)
		}
	}

	return nil, nil
}
