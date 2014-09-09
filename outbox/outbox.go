package outbox

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	linux_circus_protocol "github.com/cloudfoundry-incubator/linux-circus/protocol"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry/yagnats"
	"github.com/pivotal-golang/lager"
)

const DiegoStageFinishedSubject = "diego.staging.finished"

type Outbox struct {
	bbs        bbs.StagerBBS
	natsClient yagnats.NATSClient
	logger     lager.Logger
}

func New(bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger lager.Logger) *Outbox {
	outboxLogger := logger.Session("outbox")
	return &Outbox{
		bbs:        bbs,
		natsClient: natsClient,
		logger:     outboxLogger,
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

			if task.Domain != stager.TaskDomain {
				break
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				handleCompletedTask(task, o.bbs, o.natsClient, taskLogger)
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

func handleCompletedTask(task models.Task, bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger lager.Logger) {
	var err error

	err = bbs.ResolvingTask(task.Guid)
	if err != nil {
		logger.Error("resolving-failed", err, lager.Data{"guid": task.Guid})
		return
	}

	logger.Info("resolving-success", lager.Data{"guid": task.Guid})

	err = publishResponse(natsClient, task)
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

func publishResponse(natsClient yagnats.NATSClient, task models.Task) error {
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

		// Temporarily parse metadata until we change CC protocol
		executionMetadata := linux_circus_protocol.ExecutionMetadata{}
		err = json.Unmarshal([]byte(result.ExecutionMetadata), &executionMetadata)
		if err != nil {
			return err
		}

		response.BuildpackKey = result.BuildpackKey
		response.DetectedBuildpack = result.DetectedBuildpack
		response.DetectedStartCommand = executionMetadata.StartCommand
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return natsClient.Publish(DiegoStageFinishedSubject, payload)
}
