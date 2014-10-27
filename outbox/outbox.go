package outbox

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/api_client"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/pivotal-golang/lager"
)

const (
	// Metrics
	stagingSuccessCounter         = metric.Counter("StagingRequestsSucceeded")
	stagingSuccessDuration        = metric.Duration("StagingRequestSucceededDuration")
	stagingFailureCounter         = metric.Counter("StagingRequestsFailed")
	stagingFailureDuration        = metric.Duration("StagingRequestFailedDuration")
	stagingFailedToResolveCounter = metric.Counter("StagingFailedToResolve")

	StagingResponseRetryLimit = 3
)

type Outbox struct {
	bbs          bbs.StagerBBS
	ccClient     api_client.ApiClient
	logger       lager.Logger
	timeProvider timeprovider.TimeProvider
}

func New(bbs bbs.StagerBBS, ccClient api_client.ApiClient, logger lager.Logger, timeProvider timeprovider.TimeProvider) *Outbox {
	outboxLogger := logger.Session("outbox")

	return &Outbox{
		bbs:          bbs,
		ccClient:     ccClient,
		logger:       outboxLogger,
		timeProvider: timeProvider,
	}
}

func (o *Outbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	wg := new(sync.WaitGroup)
	tasks, stopWatching, errs := o.bbs.WatchForCompletedTask()

	taskLogger := o.logger.Session("task")
	watchLogger := taskLogger.Session("watching-for-completed-task")
	watchLogger.Info("started")

	close(ready)

	for {
		select {
		case task, ok := <-tasks:
			if !ok {
				tasks = nil
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				o.handleCompletedStagingTask(task, taskLogger)
			}()

		case err, ok := <-errs:
			if ok && err != nil {
				watchLogger.Error("failed", err)
			}

			time.Sleep(3 * time.Second)

			tasks, stopWatching, errs = o.bbs.WatchForCompletedTask()

		case <-signals:
			close(stopWatching)
			wg.Wait()
			watchLogger.Info("stopped")
			return nil
		}
	}
}

func (o *Outbox) handleCompletedStagingTask(task models.Task, logger lager.Logger) {
	var err error

	if task.Domain != stager.TaskDomain && task.Domain != stager_docker.TaskDomain {
		return
	}

	logger = logger.Session("handle-staging-complete", lager.Data{"guid": task.TaskGuid})

	err = o.bbs.ResolvingTask(task.TaskGuid)
	if err != nil {
		logger.Error("resolving-failed", err)
		return
	}
	logger.Info("resolving-success")

	duration := o.timeProvider.Time().Sub(time.Unix(0, task.CreatedAt))
	if task.Failed {
		stagingFailureCounter.Increment()
		stagingFailureDuration.Send(duration)
	} else {
		stagingSuccessDuration.Send(duration)
		stagingSuccessCounter.Increment()
	}

	response, err := o.stagingResponse(task, logger)
	if err != nil {
		logger.Error("get-staging-response-failed", err)
		return
	}

	err = o.stagingComplete(response, logger)
	if err != nil {
		logger.Error("deliver-response-failed", err)

		stagingFailedToResolveCounter.Increment()

		for i := 0; err != nil && api_client.IsRetryable(err) && i < StagingResponseRetryLimit-1; i++ {
			logger.Info("retrying-staging-complete-notification")
			err = o.stagingComplete(response, logger)
			if err != nil {
				logger.Error("retried-deliver-response-failed", err)
			}
		}

		if err != nil && api_client.IsRetryable(err) {
			return
		}
	}

	err = o.bbs.ResolveTask(task.TaskGuid)
	if err != nil {
		logger.Error("resolve-failed", err)
		return
	}

	logger.Info("resolve-success")
}

func (o *Outbox) stagingResponse(task models.Task, logger lager.Logger) ([]byte, error) {
	switch task.Domain {
	case stager.TaskDomain:
		return o.buildpackResponse(task, logger)
	case stager_docker.TaskDomain:
		return o.dockerResponse(task, logger)
	default:
		// Should never get here due to guard in function that calls this function
		panic(fmt.Sprintf("Should not try to deliver response for task domain '%s'", task.Domain))
	}
}

func (o *Outbox) buildpackResponse(task models.Task, logger lager.Logger) ([]byte, error) {
	var message cc_messages.StagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		return nil, err
	}

	message.AppId = annotation.AppId
	message.TaskId = annotation.TaskId

	if task.Failed {
		message.Error = task.FailureReason
	} else {
		var result models.StagingResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return nil, err
		}

		message.BuildpackKey = result.BuildpackKey
		message.DetectedBuildpack = result.DetectedBuildpack
		message.ExecutionMetadata = result.ExecutionMetadata
		message.DetectedStartCommand = result.DetectedStartCommand
	}

	payload, err := json.Marshal(message)
	if err != nil {
		logger.Error("marshal-error", err)
		return nil, err
	}

	return payload, nil
}

func (o *Outbox) dockerResponse(task models.Task, logger lager.Logger) ([]byte, error) {
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

	payload, err := json.Marshal(response)
	if err != nil {
		logger.Error("docker-marshal-error", err)
		return nil, err
	}

	return payload, nil
}

func (o *Outbox) stagingComplete(payload []byte, logger lager.Logger) error {
	logger.Info("posting-staging-complete", lager.Data{"payload": payload})

	err := o.ccClient.StagingComplete(payload, logger)
	if err != nil {
		logger.Error("failed-to-post-staging-complete", err)
		return err
	}

	logger.Info("posted-staging-complete")
	return nil
}
