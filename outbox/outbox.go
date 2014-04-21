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
		logger.Info("stager.watching-for-completed-runonce")
		runOnces, _, errs := bbs.WatchForCompletedTask()

	waitForTask:
		for {
			select {
			case runOnce, ok := <-runOnces:
				if !ok {
					break waitForTask
				}

				go handleCompletedTask(runOnce, bbs, natsClient, logger)
			case err, ok := <-errs:
				if ok && err != nil {
					logger.Errord(map[string]interface{}{
						"error": err.Error(),
					}, "stager.watch-completed-runonce.failed")
				}
				break waitForTask
			}
		}
	}
}

func handleCompletedTask(runOnce *models.Task, bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) {
	var err error

	err = bbs.ResolvingTask(runOnce)
	if err != nil {
		logger.Infod(map[string]interface{}{
			"guid":  runOnce.Guid,
			"error": err.Error(),
		}, "stager.resolving.runonce.failed")
		return
	}

	logger.Infod(map[string]interface{}{
		"guid": runOnce.Guid,
	}, "stager.resolving.runonce")

	err = publishResponse(natsClient, runOnce)
	if err != nil {
		logger.Errord(map[string]interface{}{
			"guid":     runOnce.Guid,
			"error":    err.Error(),
			"reply-to": runOnce.ReplyTo,
		}, "stager.publish.runonce.failed")
		return
	}

	err = bbs.ResolveTask(runOnce)
	if err != nil {
		logger.Infod(map[string]interface{}{
			"guid":  runOnce.Guid,
			"error": err.Error(),
		}, "stager.resolve.runonce.failed")
		return
	}

	logger.Infod(map[string]interface{}{
		"guid":     runOnce.Guid,
		"reply-to": runOnce.ReplyTo,
	}, "stager.resolve.runonce.success")
}

func publishResponse(natsClient yagnats.NATSClient, runOnce *models.Task) error {
	var response models.StagingResponseForCC

	if runOnce.Failed {
		response.Error = runOnce.FailureReason
	} else {
		var result models.StagingInfo
		err := json.Unmarshal([]byte(runOnce.Result), &result)
		if err != nil {
			return err
		}

		response.DetectedBuildpack = result.DetectedBuildpack
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return natsClient.Publish(runOnce.ReplyTo, payload)
}
