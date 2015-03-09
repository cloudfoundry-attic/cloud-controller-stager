package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/cloudfoundry-incubator/docker_app_lifecycle"
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
	DockerLifecycleFilename                  = "docker_app_lifecycle.zip"
	DockerStagingRequestsNatsSubject         = "diego.docker.staging.start"
	DockerStagingRequestsReceivedCounter     = metric.Counter("DockerStagingRequestsReceived")
	DockerStopStagingRequestsNatsSubject     = "diego.docker.staging.stop"
	DockerStopStagingRequestsReceivedCounter = metric.Counter("DockerStopStagingRequestsReceived")
	DockerBuilderExecutablePath              = "/tmp/docker_app_lifecycle/builder"
	DockerBuilderOutputPath                  = "/tmp/docker-result/result.json"
)

var ErrMissingDockerImageUrl = errors.New("missing docker image download url")

type dockerBackend struct {
	config Config
	logger lager.Logger
}

type consulServiceInfo struct {
	Address string
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

	//Download builder
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.DownloadAction{
				From:     compilerURL.String(),
				To:       path.Dir(DockerBuilderExecutablePath),
				CacheKey: "builder-docker",
			},
			"",
			"",
			"Failed to set up docker environment",
		),
	)

	runActionArguments := []string{"-outputMetadataJSONFilename", DockerBuilderOutputPath, "-dockerRef", request.DockerImageUrl}
	if backend.config.DockerRegistry != nil {
		registryServices, err := getDockerRegistryServices(backend.config.ConsulAgentURL)
		if err != nil {
			return receptor.TaskCreateRequest{}, err
		}

		registryRules := addDockerRegistryRules(request.EgressRules, registryServices)
		request.EgressRules = append(request.EgressRules, registryRules...)

		if backend.config.DockerRegistry.Insecure {
			registryAddresses := strings.Join(buildDockerRegistryAddresses(registryServices), ",")
			runActionArguments = append(runActionArguments, "-insecureDockerRegistries", registryAddresses)
		}
	}

	fileDescriptorLimit := uint64(request.FileDescriptors)

	// Run builder
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.RunAction{
				Path: DockerBuilderExecutablePath,
				Args: runActionArguments,
				Env:  request.Environment.BBSEnvironment(),
				ResourceLimits: models.ResourceLimits{
					Nofile: &fileDescriptorLimit,
				},
				Privileged: true,
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
		ResultFile:            DockerBuilderOutputPath,
		TaskGuid:              backend.taskGuid(request),
		Domain:                DockerTaskDomain,
		Stack:                 request.Stack,
		MemoryMB:              request.MemoryMB,
		DiskMB:                request.DiskMB,
		Action:                models.Timeout(models.Serial(actions...), dockerTimeout(request, backend.logger)),
		CompletionCallbackURL: backend.config.CallbackURL,
		LogGuid:               request.AppId,
		LogSource:             TaskLogSource,
		Annotation:            string(annotationJson),
		EgressRules:           request.EgressRules,
		Privileged:            true,
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
		Error:  backend.config.Sanitizer(errorMessage),
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
		response.Error = backend.config.Sanitizer(taskResponse.FailureReason)
	} else {
		var result docker_app_lifecycle.StagingDockerResult
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

	var lifecycleFilename string
	if len(backend.config.DockerLifecyclePath) > 0 {
		lifecycleFilename = backend.config.DockerLifecyclePath
	} else {
		lifecycleFilename = DockerLifecycleFilename
	}
	parsed, err := url.Parse(lifecycleFilename)
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

	urlString := urljoiner.Join(backend.config.FileServerURL, staticPath, lifecycleFilename)

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

func addDockerRegistryRules(egressRules []models.SecurityGroupRule, registries []consulServiceInfo) []models.SecurityGroupRule {
	for _, registry := range registries {
		egressRules = append(egressRules, models.SecurityGroupRule{
			Protocol:     models.TCPProtocol,
			Destinations: []string{registry.Address},
			Ports:        []uint16{8080},
		})
	}

	return egressRules
}

func buildDockerRegistryAddresses(services []consulServiceInfo) []string {
	registries := make([]string, 0, len(services))
	for _, service := range services {
		registries = append(registries, service.Address+":8080")
	}
	return registries
}

func getDockerRegistryServices(consulAgentURL string) ([]consulServiceInfo, error) {
	response, err := http.Get(consulAgentURL + "/v1/catalog/service/docker_registry")
	if err != nil {
		return nil, err
	}

	defer response.Body.Close()
	body, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	var ips []consulServiceInfo
	err = json.Unmarshal(body, &ips)
	if err != nil {
		return nil, err
	}

	return ips, nil
}
