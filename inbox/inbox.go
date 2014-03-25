package inbox

import (
	"encoding/json"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"

	"github.com/cloudfoundry-incubator/stager/stager"
)

type Inbox struct {
	natsClient      yagnats.NATSClient
	stager          stager.Stager
	validateRequest RequestValidator

	logger *steno.Logger
}

type RequestValidator func(models.StagingRequestFromCC) error

func Listen(natsClient yagnats.NATSClient, stager stager.Stager, validator RequestValidator, logger *steno.Logger) error {
	inbox := Inbox{
		natsClient:      natsClient,
		stager:          stager,
		validateRequest: validator,

		logger: logger,
	}

	return inbox.Listen()
}

func (inbox *Inbox) Listen() error {
	_, err := inbox.natsClient.SubscribeWithQueue("diego.staging.start", "diego.stagers", func(message *yagnats.Message) {
		stagingRequest := models.StagingRequestFromCC{}

		err := json.Unmarshal(message.Payload, &stagingRequest)
		if err != nil {
			inbox.logError("staging.request.malformed", err, message)
			inbox.sendErrorResponse(message.ReplyTo, "Staging message contained malformed JSON")
			return
		}

		err = inbox.validateRequest(stagingRequest)
		if err != nil {
			inbox.logError("staging.request.invalid", err, message)
			inbox.sendErrorResponse(message.ReplyTo, "Invalid staging request: "+err.Error())
			return
		}

		inbox.logger.Infod(
			map[string]interface{}{
				"message": stagingRequest,
			},
			"staging.request.received",
		)

		err = inbox.stager.Stage(stagingRequest, message.ReplyTo)
		if err != nil {
			inbox.logError("stager.staging.failed", err, stagingRequest)
			inbox.sendErrorResponse(message.ReplyTo, "Staging failed: "+err.Error())
			return
		}

	})

	return err
}

func (inbox *Inbox) logError(logMessage string, err error, message interface{}) {
	inbox.logger.Errord(map[string]interface{}{
		"message": message,
		"error":   err.Error(),
	}, logMessage)
}

func (inbox *Inbox) sendErrorResponse(replyTo string, errorMessage string) {
	response := models.StagingResponseForCC{Error: errorMessage}
	if responseJson, err := json.Marshal(response); err == nil {
		inbox.natsClient.Publish(replyTo, responseJson)
	}
}
