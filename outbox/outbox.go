package outbox

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

const DiegoStageFinishedSubject = "diego.staging.finished"

type Outbox struct {
	bbs        bbs.StagerBBS
	natsClient yagnats.NATSClient
	logger     *steno.Logger
}

func New(bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) *Outbox {
	return &Outbox{
		bbs:        bbs,
		natsClient: natsClient,
		logger:     logger,
	}
}

func (o *Outbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	wg := new(sync.WaitGroup)
	tasks, stopWatching, errs := o.bbs.WatchForCompletedTask()

	o.logger.Info("stager.watching-for-completed-task")
	close(ready)

	for {
		select {
		case task, ok := <-tasks:
			if !ok {
				tasks = nil
			}

			if task.Type != models.TaskTypeStaging {
				break
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				handleCompletedTask(task, o.bbs, o.natsClient, o.logger)
			}()

		case err, ok := <-errs:
			if ok && err != nil {
				o.logger.Errord(map[string]interface{}{
					"error": err.Error(),
				}, "stager.watch-completed-task.failed")
			}

			time.Sleep(3 * time.Second)

			tasks, stopWatching, errs = o.bbs.WatchForCompletedTask()

		case <-signals:
			close(stopWatching)
			wg.Wait()
			return nil
		}
	}
}

func handleCompletedTask(task models.Task, bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) {
	var err error

	task, err = bbs.ResolvingTask(task)
	if err != nil {
		logger.Infod(map[string]interface{}{
			"guid":  task.Guid,
			"error": err.Error(),
		}, "stager.resolving.task.failed")
		return
	}

	logger.Infod(map[string]interface{}{
		"guid": task.Guid,
	}, "stager.resolving.task")

	err = publishResponse(natsClient, task)
	if err != nil {
		logger.Errord(map[string]interface{}{
			"guid":  task.Guid,
			"error": err.Error(),
		}, "stager.publish.task.failed")
		return
	}

	task, err = bbs.ResolveTask(task)
	if err != nil {
		logger.Infod(map[string]interface{}{
			"guid":  task.Guid,
			"error": err.Error(),
		}, "stager.resolve.task.failed")
		return
	}

	logger.Infod(map[string]interface{}{
		"guid": task.Guid,
	}, "stager.resolve.task.success")
}

func publishResponse(natsClient yagnats.NATSClient, task models.Task) error {
	var response models.StagingResponseForCC

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
		var result models.StagingInfo
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return err
		}

		response.BuildpackKey = result.BuildpackKey
		response.DetectedBuildpack = result.DetectedBuildpack
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return natsClient.Publish(DiegoStageFinishedSubject, payload)
}
