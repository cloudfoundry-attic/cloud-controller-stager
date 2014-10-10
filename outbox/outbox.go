package outbox

import (
	"encoding/json"
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
	stagingSuccessCounter  = metric.Counter("StagingRequestsSucceeded")
	stagingSuccessDuration = metric.Duration("StagingRequestSucceededDuration")
	stagingFailureCounter  = metric.Counter("StagingRequestsFailed")
	stagingFailureDuration = metric.Duration("StagingRequestFailedDuration")
)

type Outbox struct {
	bbs          bbs.StagerBBS
	apiClient    api_client.ApiClient
	logger       lager.Logger
	timeProvider timeprovider.TimeProvider
}

func New(bbs bbs.StagerBBS, apiClient api_client.ApiClient, logger lager.Logger, timeProvider timeprovider.TimeProvider) *Outbox {
	outboxLogger := logger.Session("outbox")

	return &Outbox{
		bbs:          bbs,
		apiClient:    apiClient,
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
				o.handleCompletedStagingTask(task, o.bbs, taskLogger, o.timeProvider)
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

func (o *Outbox) handleCompletedStagingTask(
	task models.Task,
	bbs bbs.StagerBBS,
	logger lager.Logger,
	timeProvider timeprovider.TimeProvider,
) {
	var err error

	if task.Domain != stager.TaskDomain && task.Domain != stager_docker.TaskDomain {
		return
	}

	duration := timeProvider.Time().Sub(time.Unix(0, task.CreatedAt))

	if task.Failed {
		stagingFailureCounter.Increment()
		stagingFailureDuration.Send(duration)
	} else {
		stagingSuccessDuration.Send(duration)
		stagingSuccessCounter.Increment()
	}
	err = bbs.ResolvingTask(task.Guid)
	if err != nil {
		logger.Error("resolving-failed", err, lager.Data{"guid": task.Guid})
		return
	}

	logger.Info("resolving-success", lager.Data{"guid": task.Guid})

	if task.Domain == stager.TaskDomain {
		err = o.deliverResponse(task, logger.Session("staging-response"))
	} else {
		err = o.publishDockerResponse(task, logger.Session("docker-staging-response"))
	}

	if err != nil {
		logger.Error("publishing-failed", err, lager.Data{"guid": task.Guid})
		return
	}

	err = bbs.ResolveTask(task.Guid)
	if err != nil {
		logger.Error("resolve-failed", err, lager.Data{"guid": task.Guid})
		return
	}
	logger.Info("resolve-success", lager.Data{"guid": task.Guid})
}

func (o *Outbox) deliverResponse(task models.Task, logger lager.Logger) error {
	var message cc_messages.StagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		return err
	}

	message.AppId = annotation.AppId
	message.TaskId = annotation.TaskId

	if task.Failed {
		message.Error = task.FailureReason
	} else {
		var result models.StagingResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return err
		}

		message.BuildpackKey = result.BuildpackKey
		message.DetectedBuildpack = result.DetectedBuildpack
		message.ExecutionMetadata = result.ExecutionMetadata
		message.DetectedStartCommand = result.DetectedStartCommand
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	err = o.apiClient.StagingComplete(payload, logger)
	if err != nil {
		return err
	}

	return nil
}

func (o *Outbox) publishDockerResponse(task models.Task, logger lager.Logger) error {
	var response cc_messages.DockerStagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		return err
	}

	response.AppId = annotation.AppId
	response.TaskId = annotation.TaskId

	if task.Failed {
		response.Error = task.FailureReason
	} else {
		var result models.StagingDockerResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return err
		}
		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	err = o.apiClient.StagingComplete(payload, logger)
	if err != nil {
		return err
	}

	return nil
}
