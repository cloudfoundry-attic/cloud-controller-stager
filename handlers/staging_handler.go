package handlers

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/pivotal-golang/lager"
)

const (
	StagingStartRequestsReceivedCounter = metric.Counter("StagingStartRequestsReceived")
	StagingStopRequestsReceivedCounter  = metric.Counter("StagingStopRequestsReceived")
)

type StagingHandler interface {
	Stage(resp http.ResponseWriter, req *http.Request)
	StopStaging(resp http.ResponseWriter, req *http.Request)
}

type stagingHandler struct {
	logger      lager.Logger
	backends    map[string]backend.Backend
	ccClient    cc_client.CcClient
	diegoClient receptor.Client
}

func NewStagingHandler(
	logger lager.Logger,
	backends map[string]backend.Backend,
	ccClient cc_client.CcClient,
	diegoClient receptor.Client,
) StagingHandler {
	logger = logger.Session("staging-handler")

	return &stagingHandler{
		logger:      logger,
		backends:    backends,
		ccClient:    ccClient,
		diegoClient: diegoClient,
	}
}

func (handler *stagingHandler) Stage(resp http.ResponseWriter, req *http.Request) {
	stagingGuid := req.FormValue(":staging_guid")
	logger := handler.logger.Session("staging-request", lager.Data{"staging-guid": stagingGuid})

	requestBody, err := ioutil.ReadAll(req.Body)
	if err != nil {
		logger.Error("read-body-failed", err)
		resp.WriteHeader(http.StatusInternalServerError)
		return
	}

	var stagingRequest cc_messages.StagingRequestFromCC
	err = json.Unmarshal(requestBody, &stagingRequest)
	if err != nil {
		logger.Error("unmarshal-request-failed", err)
		resp.WriteHeader(http.StatusBadRequest)
		return
	}

	backend, ok := handler.backends[stagingRequest.Lifecycle]
	if !ok {
		logger.Error("backend-not-found", err, lager.Data{"backend": stagingRequest.Lifecycle})
		resp.WriteHeader(http.StatusNotFound)
		return
	}

	StagingStartRequestsReceivedCounter.Increment()

	taskRequest, err := backend.BuildRecipe(stagingGuid, stagingRequest)
	if err != nil {
		logger.Error("recipe-building-failed", err, lager.Data{"staging-request": stagingRequest})
		handler.doErrorResponse(resp, "Recipe building failed: "+err.Error())
		return
	}

	logger.Info("desiring-task", lager.Data{
		"task_guid":    taskRequest.TaskGuid,
		"callback_url": taskRequest.CompletionCallbackURL,
	})

	err = handler.diegoClient.CreateTask(taskRequest)
	if receptorErr, ok := err.(receptor.Error); ok {
		if receptorErr.Type == receptor.TaskGuidAlreadyExists {
			err = nil
		}
	}

	if err != nil {
		logger.Error("staging-failed", err, lager.Data{"staging-request": stagingRequest})
		handler.doErrorResponse(resp, "Staging failed: "+err.Error())
		return
	}

	resp.WriteHeader(http.StatusAccepted)
}

func (handler *stagingHandler) doErrorResponse(resp http.ResponseWriter, message string) {
	response := cc_messages.StagingResponseForCC{
		Error: cc_messages.SanitizeErrorMessage(message),
	}
	responseJson, _ := json.Marshal(response)

	resp.WriteHeader(http.StatusInternalServerError)
	resp.Write(responseJson)
}

func (handler *stagingHandler) StopStaging(resp http.ResponseWriter, req *http.Request) {
	taskGuid := req.FormValue(":staging_guid")
	logger := handler.logger.Session("stop-staging-request", lager.Data{"staging-guid": taskGuid})

	task, err := handler.diegoClient.GetTask(taskGuid)
	if err != nil {
		if receptorErr, ok := err.(receptor.Error); ok {
			if receptorErr.Type == receptor.TaskNotFound {
				resp.WriteHeader(http.StatusNotFound)
				return
			}
		}

		logger.Error("failed-to-get-task", err)
		resp.WriteHeader(http.StatusInternalServerError)
		return
	}

	var annotation cc_messages.StagingTaskAnnotation
	err = json.Unmarshal([]byte(task.Annotation), &annotation)
	if err != nil {
		logger.Error("failed-to-unmarshal-task-annotation", err)
		resp.WriteHeader(http.StatusInternalServerError)
		return
	}

	resp.WriteHeader(http.StatusAccepted)
	StagingStopRequestsReceivedCounter.Increment()

	logger.Info("cancelling", lager.Data{"task_guid": taskGuid})

	err = handler.diegoClient.CancelTask(taskGuid)
	if err != nil {
		logger.Error("stop-staging-failed", err)
	}
}
