package outbox

import (
	"encoding/json"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

func Listen(bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) {
	for {
		logger.Info("stager.watching-for-completed-task")
		tasks, _, errs := bbs.WatchForCompletedTask()

	waitForTask:
		for {
			select {
			case task, ok := <-tasks:
				if !ok {
					break waitForTask
				}

				go handleCompletedTask(task, bbs, natsClient, logger)
			case err, ok := <-errs:
				if ok && err != nil {
					logger.Errord(map[string]interface{}{
						"error": err.Error(),
					}, "stager.watch-completed-task.failed")
				}
				break waitForTask
			}
		}
	}
}

func handleCompletedTask(task *models.Task, bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) {
	var err error

	err = bbs.ResolvingTask(task)
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
			"guid":     task.Guid,
			"error":    err.Error(),
			"reply-to": task.ReplyTo,
		}, "stager.publish.task.failed")
		return
	}

	err = bbs.ResolveTask(task)
	if err != nil {
		logger.Infod(map[string]interface{}{
			"guid":  task.Guid,
			"error": err.Error(),
		}, "stager.resolve.task.failed")
		return
	}

	logger.Infod(map[string]interface{}{
		"guid":     task.Guid,
		"reply-to": task.ReplyTo,
	}, "stager.resolve.task.success")
}

func publishResponse(natsClient yagnats.NATSClient, task *models.Task) error {
	var response models.StagingResponseForCC

	if task.Failed {
		response.Error = task.FailureReason
	} else {
		var result models.StagingInfo
		err := json.Unmarshal([]byte(task.Result), &result)
		if err != nil {
			return err
		}

		response.DetectedBuildpack = result.DetectedBuildpack
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return natsClient.Publish(task.ReplyTo, payload)
}
