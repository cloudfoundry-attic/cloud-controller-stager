package outbox

import (
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
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
					natsClient.Publish(runOnce.ReplyTo, []byte("{}"))
					logger.Infod(map[string]interface{}{
						"guid":     runOnce.Guid,
						"reply-to": runOnce.ReplyTo,
					}, "stager.resolve.runonce.success")
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
