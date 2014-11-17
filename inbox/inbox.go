package inbox

import (
	"os"
	"time"

	"github.com/apcera/nats"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
)

type Inbox struct {
	natsClient  diegonats.NATSClient
	ccClient    cc_client.CcClient
	diegoClient receptor.Client
	logger      lager.Logger
	backend     backend.Backend
}

func New(natsClient diegonats.NATSClient, ccClient cc_client.CcClient, diegoClient receptor.Client, backend backend.Backend, logger lager.Logger) *Inbox {
	inboxLogger := logger.Session("inbox", lager.Data{"TaskDomain": backend.TaskDomain()})
	return &Inbox{
		natsClient:  natsClient,
		ccClient:    ccClient,
		diegoClient: diegoClient,
		logger:      inboxLogger,
		backend:     backend,
	}
}

func (inbox *Inbox) Run(signals <-chan os.Signal, ready chan<- struct{}) error {
	inbox.subscribeStagingStart()
	inbox.subscribeStagingStop()

	close(ready)

	<-signals

	return nil
}

func (inbox *Inbox) subscribeStagingStart() {
	for {
		_, err := inbox.natsClient.Subscribe(inbox.backend.StagingRequestsNatsSubject(), inbox.onStagingRequest)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (inbox *Inbox) subscribeStagingStop() {
	for {
		_, err := inbox.natsClient.Subscribe(inbox.backend.StopStagingRequestsNatsSubject(), inbox.onStopStagingRequest)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (inbox *Inbox) onStagingRequest(message *nats.Msg) {
	requestLogger := inbox.logger.Session("staging-request")
	inbox.backend.StagingRequestsReceivedCounter().Increment()

	taskRequest, err := inbox.backend.BuildRecipe(message.Data)
	if err != nil {
		requestLogger.Error("recipe-building-failed", err, lager.Data{"message": message.Data})
		inbox.sendStagingCompleteError("Recipe building failed: ", err, message.Data)
		return
	}

	requestLogger.Info("desiring-task", lager.Data{
		"task_guid":    taskRequest.TaskGuid,
		"callback_url": taskRequest.CompletionCallbackURL,
	})
	err = inbox.diegoClient.CreateTask(taskRequest)
	if receptorErr, ok := err.(receptor.Error); ok {
		if receptorErr.Type == receptor.TaskGuidAlreadyExists {
			err = nil
		}
	}

	if err != nil {
		requestLogger.Error("staging-failed", err, lager.Data{"message": message.Data})
		inbox.sendStagingCompleteError("Staging failed: ", err, message.Data)
	}
}

func (inbox *Inbox) onStopStagingRequest(message *nats.Msg) {
	requestLogger := inbox.logger.Session("stop-staging-request")
	inbox.backend.StopStagingRequestsReceivedCounter().Increment()

	taskGuid, err := inbox.backend.StagingTaskGuid(message.Data)
	if err != nil {
		requestLogger.Error("staging-task-guid-failed", err, lager.Data{"message": message.Data})
		return
	}

	requestLogger.Info("cancelling", lager.Data{"task_guid": taskGuid})

	err = inbox.diegoClient.CancelTask(taskGuid)
	if err != nil {
		requestLogger.Error("stop-staging-failed", err, lager.Data{"message": message.Data})
	}
}

func (inbox *Inbox) sendStagingCompleteError(messagePrefix string, err error, requestJson []byte) {
	responseJson, err := inbox.backend.BuildStagingResponseFromRequestError(requestJson, messagePrefix+err.Error())
	if err == nil {
		inbox.ccClient.StagingComplete(responseJson, inbox.logger)
	}
}
