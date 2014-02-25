package outbox

import (
	"encoding/json"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/stager"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
)

func Listen(bbs bbs.StagerBBS, natsClient yagnats.NATSClient, logger *steno.Logger) {
	for {
		runOnces, _, errs := bbs.WatchForCompletedRunOnce()
	dance:
		for {
			select {
			case runOnce := <-runOnces:
				logger.Infod(map[string]interface{}{
					"guid": runOnce.Guid,
				}, "stager.resolve.runonce")

				err := bbs.ResolveRunOnce(runOnce)

				if err == nil {
					logger.Infod(map[string]interface{}{
						"guid":     runOnce.Guid,
						"reply-to": runOnce.ReplyTo,
					}, "stager.resolve.runonce.success")

					err := publishResponse(natsClient, runOnce)
					if err != nil {
						logger.Errord(map[string]interface{}{
							"guid":     runOnce.Guid,
							"error":    err.Error(),
							"reply-to": runOnce.ReplyTo,
						}, "stager.publish.runonce.failed")
					}
				} else {
					logger.Errord(map[string]interface{}{
						"guid":  runOnce.Guid,
						"error": err.Error(),
					}, "stager.resolve.runonce.failed")
				}
			case err := <-errs:
				logger.Warnf("error watching for completions: %s\n", err)
				break dance
			}
		}
	}
}

func publishResponse(natsClient yagnats.NATSClient, runOnce models.RunOnce) error {
	var response stager.StagingResponse

	if runOnce.Failed {
		response.Error = runOnce.FailureReason
	} else {
		var result stager.StagingResult
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
