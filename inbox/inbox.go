package inbox

import (
	"encoding/json"
	"os"
	"time"

	"github.com/apcera/nats"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
)

const DiegoStageStartSubject = "diego.staging.start"
const DiegoDockerStageStartSubject = "diego.docker.staging.start"

type Inbox struct {
	natsClient      diegonats.NATSClient
	stager          stager.Stager
	ccClient        cc_client.CcClient
	validateRequest RequestValidator
	dockerStager    stager_docker.DockerStager
	logger          lager.Logger
}

type RequestValidator func(cc_messages.StagingRequestFromCC) error

func New(natsClient diegonats.NATSClient, ccClient cc_client.CcClient, stager stager.Stager, dockerStager stager_docker.DockerStager, validator RequestValidator, logger lager.Logger) *Inbox {
	inboxLogger := logger.Session("inbox")
	return &Inbox{
		natsClient:      natsClient,
		stager:          stager,
		ccClient:        ccClient,
		validateRequest: validator,
		dockerStager:    dockerStager,
		logger:          inboxLogger,
	}
}

func (inbox *Inbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	inbox.subscribeStagingStart()
	inbox.subscribeDockerStagingStart()

	close(ready)

	<-signals

	return nil
}

func (inbox *Inbox) subscribeDockerStagingStart() {
	for {
		_, err := inbox.natsClient.Subscribe(DiegoDockerStageStartSubject, inbox.onDockerStagingRequest)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	return
}

func (inbox *Inbox) onDockerStagingRequest(message *nats.Msg) {
	requestLogger := inbox.logger.Session("docker-request")
	stagingRequest := cc_messages.DockerStagingRequestFromCC{}

	err := json.Unmarshal(message.Data, &stagingRequest)
	if err != nil {
		requestLogger.Error("malformed docker request", err, lager.Data{"message": message})
		return
	}

	requestLogger.Info("received", lager.Data{"message": stagingRequest})

	err = inbox.dockerStager.Stage(stagingRequest)

	if err != nil {
		response := cc_messages.DockerStagingResponseForCC{
			AppId:  stagingRequest.AppId,
			TaskId: stagingRequest.TaskId,
			Error:  "Staging failed: " + err.Error(),
		}

		if responseJson, err := json.Marshal(response); err == nil {
			inbox.ccClient.StagingComplete(responseJson, inbox.logger)
		}
	}
}

func (inbox *Inbox) subscribeStagingStart() {
	for {
		_, err := inbox.natsClient.Subscribe(DiegoStageStartSubject, inbox.onStagingRequest)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	return
}

func (inbox *Inbox) onStagingRequest(message *nats.Msg) {
	requestLogger := inbox.logger.Session("request")
	stagingRequest := cc_messages.StagingRequestFromCC{}

	err := json.Unmarshal(message.Data, &stagingRequest)
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

func (inbox *Inbox) sendErrorResponse(errorMessage string, request cc_messages.StagingRequestFromCC) {
	response := cc_messages.StagingResponseForCC{
		AppId:  request.AppId,
		TaskId: request.TaskId,
		Error:  errorMessage,
	}

	if responseJson, err := json.Marshal(response); err == nil {
		inbox.ccClient.StagingComplete(responseJson, inbox.logger)
	}
}
