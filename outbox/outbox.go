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
				err := bbs.ResolveRunOnce(runOnce)
				if err == nil {
					natsClient.Publish(runOnce.ReplyTo, []byte("{}"))
				}
			case err := <-errs:
				logger.Warnf("error watching for completions: %s\n", err)
				break dance
			}
		}
	}
}
