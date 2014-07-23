package inbox

import (
	"encoding/json"
	"os"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry/yagnats"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
)

const DiegoStageStartSubject = "diego.staging.start"

type Inbox struct {
	natsClient      yagnats.NATSClient
	stager          stager.Stager
	validateRequest RequestValidator

	logger lager.Logger
}

type RequestValidator func(models.StagingRequestFromCC) error

func New(natsClient yagnats.NATSClient, stager stager.Stager, validator RequestValidator, logger lager.Logger) *Inbox {
	inboxLogger := logger.Session("inbox")
	return &Inbox{
		natsClient:      natsClient,
		stager:          stager,
		validateRequest: validator,

		logger: inboxLogger,
	}
}

func (inbox *Inbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	inbox.subscribe()

	close(ready)

	<-signals

	return nil
}

func (inbox *Inbox) subscribe() {
	for {
		_, err := inbox.natsClient.Subscribe(DiegoStageStartSubject, inbox.onStagingRequest)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	return
}

func (inbox *Inbox) onStagingRequest(message *yagnats.Message) {
	requestLogger := inbox.logger.Session("request")
	stagingRequest := models.StagingRequestFromCC{}

	err := json.Unmarshal(message.Payload, &stagingRequest)
	if err != nil {
		requestLogger.Error("malformed", err, lager.Data{"message": message})
		return
	}

	err = inbox.validateRequest(stagingRequest)
	if err != nil {
		requestLogger.Error("invalid", err, lager.Data{"message": message})
		inbox.sendErrorResponse("Invalid staging request: "+err.Error(), stagingRequest)
		return
	}

	requestLogger.Info("received", lager.Data{"message": stagingRequest})

	err = inbox.stager.Stage(stagingRequest)
	if err != nil {
		requestLogger.Error("staging-failed", err, lager.Data{"message": stagingRequest})
		inbox.sendErrorResponse("Staging failed: "+err.Error(), stagingRequest)
		return
	}
}

func (inbox *Inbox) sendErrorResponse(errorMessage string, request models.StagingRequestFromCC) {
	response := models.StagingResponseForCC{
		AppId:  request.AppId,
		TaskId: request.TaskId,
		Error:  errorMessage,
	}

	if responseJson, err := json.Marshal(response); err == nil {
		inbox.natsClient.Publish(outbox.DiegoStageFinishedSubject, responseJson)
	}
}
