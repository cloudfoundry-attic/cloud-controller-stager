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
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
	"github.com/cloudfoundry/gunk/timeprovider"
	"github.com/cloudfoundry/yagnats"
	"github.com/pivotal-golang/lager"
)

const (
	// Metrics
	stagingMsgSuccessCounter  = metric.Counter("staging-message-success")
	stagingMsgSuccessDuration = metric.Duration("staging-message-success-duration")
	stagingMsgFailureCounter  = metric.Counter("staging-message-failure")
	stagingMsgFailureDuration = metric.Duration("staging-message-failure-duration")

	// NATS subjects
	DiegoStageFinishedSubject       = "diego.staging.finished"
	DiegoDockerStageFinishedSubject = "diego.docker.staging.finished"
)

type Outbox struct {
	bbs          bbs.StagerBBS
	natsClient   yagnats.NATSClient
	logger       lager.Logger
	timeProvider timeprovider.TimeProvider
}

func New(bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger lager.Logger, timeProvider timeprovider.TimeProvider) *Outbox {
	outboxLogger := logger.Session("outbox")
	return &Outbox{
		bbs:          bbs,
		natsClient:   natsClient,
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
				handleCompletedStagingTask(task, o.bbs, o.natsClient, taskLogger, o.timeProvider)
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

func handleCompletedStagingTask(
	task models.Task,
	bbs bbs.StagerBBS,
	natsClient yagnats.NATSClient,
	logger lager.Logger,
	timeProvider timeprovider.TimeProvider,
) {
	var err error

	if task.Domain != stager.TaskDomain && task.Domain != stager_docker.TaskDomain {
		return
	}

	duration := timeProvider.Time().Sub(time.Unix(0, task.CreatedAt))

	if task.Failed {
		stagingMsgFailureCounter.Increment()
		stagingMsgFailureDuration.Send(duration)
	} else {
		stagingMsgSuccessDuration.Send(duration)
		stagingMsgSuccessCounter.Increment()
	}
	err = bbs.ResolvingTask(task.Guid)
	if err != nil {
		logger.Error("resolving-failed", err, lager.Data{"guid": task.Guid})
		return
	}

	logger.Info("resolving-success", lager.Data{"guid": task.Guid})

	if task.Domain == stager.TaskDomain {
		err = publishResponse(natsClient, task, logger)
	} else {
		err = publishDockerResponse(natsClient, task, logger)
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

func publishResponse(natsClient yagnats.NATSClient, task models.Task, logger lager.Logger) error {
	var response cc_messages.StagingResponseForCC

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
		var result models.StagingResult
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return err
		}

		response.BuildpackKey = result.BuildpackKey
		response.DetectedBuildpack = result.DetectedBuildpack
		response.ExecutionMetadata = result.ExecutionMetadata
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	logger.Info("publish-success", lager.Data{"payload": payload})

	return natsClient.Publish(DiegoStageFinishedSubject, payload)
}

func publishDockerResponse(natsClient yagnats.NATSClient, task models.Task, logger lager.Logger) error {
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
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	logger.Info("publish-docker-success", lager.Data{"payload": payload})

	return natsClient.Publish(DiegoDockerStageFinishedSubject, payload)

}
