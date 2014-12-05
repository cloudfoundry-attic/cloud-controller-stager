package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/routes"
	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
)

const (
	DockerTaskDomain                         = "cf-app-docker-staging"
	DockerCircusFilename                     = "docker-circus.zip"
	DockerStagingRequestsNatsSubject         = "diego.docker.staging.start"
	DockerStagingRequestsReceivedCounter     = metric.Counter("DockerStagingRequestsReceived")
	DockerStopStagingRequestsNatsSubject     = "diego.docker.staging.stop"
	DockerStopStagingRequestsReceivedCounter = metric.Counter("DockerStopStagingRequestsReceived")
	DockerTailorExecutablePath               = "/tmp/docker-circus/tailor"
	DockerTailorOutputPath                   = "/tmp/docker-result/result.json"
)

var ErrMissingDockerImageUrl = errors.New("missing docker image download url")

type dockerBackend struct {
	config Config
	logger lager.Logger
}

func NewDockerBackend(config Config, logger lager.Logger) Backend {
	return &dockerBackend{
		config: config,
		logger: logger.Session("docker"),
	}
}

func (backend *dockerBackend) StagingRequestsNatsSubject() string {
	return DockerStagingRequestsNatsSubject
}

func (backend *dockerBackend) StagingRequestsReceivedCounter() metric.Counter {
	return DockerStagingRequestsReceivedCounter
}

func (backend *dockerBackend) StopStagingRequestsNatsSubject() string {
	return DockerStopStagingRequestsNatsSubject
}

func (backend *dockerBackend) StopStagingRequestsReceivedCounter() metric.Counter {
	return DockerStopStagingRequestsReceivedCounter
}

func (backend *dockerBackend) TaskDomain() string {
	return DockerTaskDomain
}

func (backend *dockerBackend) BuildRecipe(requestJson []byte) (receptor.TaskCreateRequest, error) {
	logger := backend.logger.Session("build-recipe")

	var request cc_messages.DockerStagingRequestFromCC
	err := json.Unmarshal(requestJson, &request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}
	logger.Info("staging-request", lager.Data{"Request": request})

	err = backend.validateRequest(request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	compilerURL, err := backend.compilerDownloadURL(request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	actions := []models.Action{}

	//Download tailor
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.DownloadAction{
				From:     compilerURL.String(),
				To:       path.Dir(DockerTailorExecutablePath),
				CacheKey: "tailor-docker",
			},
			"",
			"",
			"Failed to set up docker environment",
		),
	)

	var fileDescriptorLimit *uint64
	if request.FileDescriptors != 0 {
		fd := max(uint64(request.FileDescriptors), backend.config.MinFileDescriptors)
		fileDescriptorLimit = &fd
	}

	//Run Smelter
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.RunAction{
				Path: DockerTailorExecutablePath,
				Args: []string{"-outputMetadataJSONFilename", DockerTailorOutputPath, "-dockerRef", request.DockerImageUrl},
				Env:  request.Environment.BBSEnvironment(),
				ResourceLimits: models.ResourceLimits{
					Nofile: fileDescriptorLimit,
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		),
	)

	annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
		AppId:  request.AppId,
		TaskId: request.TaskId,
	})

	task := receptor.TaskCreateRequest{
		ResultFile:            DockerTailorOutputPath,
		TaskGuid:              backend.taskGuid(request),
		Domain:                DockerTaskDomain,
		Stack:                 request.Stack,
		MemoryMB:              int(max(uint64(request.MemoryMB), uint64(backend.config.MinMemoryMB))),
		DiskMB:                int(max(uint64(request.DiskMB), uint64(backend.config.MinDiskMB))),
		Action:                models.Timeout(models.Serial(actions...), dockerTimeout(request, backend.logger)),
		CompletionCallbackURL: backend.config.CallbackURL,
		LogGuid:               request.AppId,
		LogSource:             TaskLogSource,
		Annotation:            string(annotationJson),
	}

	logger.Debug("staging-task-request", lager.Data{"TaskCreateRequest": task})

	return task, nil
}

func (backend *dockerBackend) BuildStagingResponseFromRequestError(requestJson []byte, errorMessage string) ([]byte, error) {
	request := cc_messages.DockerStagingRequestFromCC{}

	err := json.Unmarshal(requestJson, &request)
	if err != nil {
		return nil, err
	}

	response := cc_messages.DockerStagingResponseForCC{
		AppId:  request.AppId,
		TaskId: request.TaskId,
		Error:  errorMessage,
	}

	return json.Marshal(response)
}

func (backend *dockerBackend) BuildStagingResponse(taskResponse receptor.TaskResponse) ([]byte, error) {
	var response cc_messages.DockerStagingResponseForCC

	var annotation models.StagingTaskAnnotation
	err := json.Unmarshal([]byte(taskResponse.Annotation), &annotation)
	if err != nil {
		return nil, err
	}

	response.AppId = annotation.AppId
	response.TaskId = annotation.TaskId

	if taskResponse.Failed {
		response.Error = taskResponse.FailureReason
	} else {
		var result models.StagingDockerResult
		err := json.Unmarshal([]byte(taskResponse.Result), &result)
		if err != nil {
			return nil, err
		}

		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	return json.Marshal(response)
}

func (backend *dockerBackend) StagingTaskGuid(requestJson []byte) (string, error) {
	var request cc_messages.StopStagingRequestFromCC
	err := json.Unmarshal(requestJson, &request)
	if err != nil {
		return "", err
	}

	if request.AppId == "" {
		return "", ErrMissingAppId
	}

	if request.TaskId == "" {
		return "", ErrMissingTaskId
	}

	return stagingTaskGuid(request.AppId, request.TaskId), nil
}

func (backend *dockerBackend) compilerDownloadURL(request cc_messages.DockerStagingRequestFromCC) (*url.URL, error) {

	var circusFilename string
	if len(backend.config.DockerCircusPath) > 0 {
		circusFilename = backend.config.DockerCircusPath
	} else {
		circusFilename = DockerCircusFilename
	}
	parsed, err := url.Parse(circusFilename)
	if err != nil {
		return nil, errors.New("couldn't parse compiler URL")
	}

	switch parsed.Scheme {
	case "http", "https":
		return parsed, nil
	case "":
		break
	default:
		return nil, fmt.Errorf("unknown scheme: '%s'", parsed.Scheme)
	}

	staticPath, err := routes.FileServerRoutes.CreatePathForRoute(routes.FS_STATIC, nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate the compiler download path: %s", err)
	}

	urlString := urljoiner.Join(backend.config.FileServerURL, staticPath, circusFilename)

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse compiler download URL: %s", err)
	}

	return url, nil
}

func (backend *dockerBackend) taskGuid(request cc_messages.DockerStagingRequestFromCC) string {
	return stagingTaskGuid(request.AppId, request.TaskId)
}

func (backend *dockerBackend) validateRequest(stagingRequest cc_messages.DockerStagingRequestFromCC) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(stagingRequest.TaskId) == 0 {
		return ErrMissingTaskId
	}

	if len(stagingRequest.DockerImageUrl) == 0 {
		return ErrMissingDockerImageUrl
	}

	return nil
}

func dockerTimeout(request cc_messages.DockerStagingRequestFromCC, logger lager.Logger) time.Duration {
	if request.Timeout > 0 {
		return time.Duration(request.Timeout) * time.Second
	} else {
		logger.Info("overriding requested timeout", lager.Data{
			"requested-timeout": request.Timeout,
			"default-timeout":   DefaultStagingTimeout,
			"app-id":            request.AppId,
			"task-id":           request.TaskId,
		})
		return DefaultStagingTimeout
	}
}
