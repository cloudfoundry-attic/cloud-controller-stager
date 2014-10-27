package outbox

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/tedsuo/ifrit/http_server"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
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
	logger       lager.Logger
	timeProvider timeprovider.TimeProvider
}

func New(address string, ccClient cc_client.CcClient, logger lager.Logger, timeProvider timeprovider.TimeProvider) *Outbox {
	outboxLogger := logger.Session("outbox")

	return &Outbox{
		address:      address,
		ccClient:     ccClient,
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

	response, err := o.stagingResponse(task)
	if err != nil {
		logger.Error("get-staging-response-failed", err)
		return
	}

	if response == nil {
		res.WriteHeader(404)
		res.Write([]byte("Unknown task domain"))
		return
	}

	payload, err := json.Marshal(response)
	if err != nil {
		logger.Error("marshal-error", err)
		res.WriteHeader(http.StatusOK)
		return
	}

	logger.Info("posting-staging-complete", lager.Data{
		"payload": payload,
	})

	err = o.ccClient.StagingComplete(payload, logger)
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
	duration := o.timeProvider.Time().Sub(time.Unix(0, task.CreatedAt))
	if task.Failed {
		stagingFailureCounter.Increment()
		stagingFailureDuration.Send(duration)
	} else {
		stagingSuccessDuration.Send(duration)
		stagingSuccessCounter.Increment()
	}
}

func (o *Outbox) stagingResponse(task receptor.TaskResponse) (interface{}, error) {
	switch task.Domain {
	case stager.TaskDomain:
		return o.buildpackStagingResponse(task)
	case stager_docker.TaskDomain:
		return o.dockerStagingResponse(task)
	default:
		return nil, nil
	}
}

func (o *Outbox) buildpackStagingResponse(task receptor.TaskResponse) (interface{}, error) {
	var response cc_messages.StagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		return nil, err
	}

	response.AppId = annotation.AppId
	response.TaskId = annotation.TaskId

	if task.Failed {
		response.Error = task.FailureReason
	} else {
		var result models.StagingResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return nil, err
		}

		response.BuildpackKey = result.BuildpackKey
		response.DetectedBuildpack = result.DetectedBuildpack
		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	return response, nil
}

func (o *Outbox) dockerStagingResponse(task receptor.TaskResponse) (interface{}, error) {
	var response cc_messages.DockerStagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		return nil, err
	}

	response.AppId = annotation.AppId
	response.TaskId = annotation.TaskId

	if task.Failed {
		response.Error = task.FailureReason
	} else {
		var result models.StagingDockerResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return nil, err
		}
		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	return response, nil
}
