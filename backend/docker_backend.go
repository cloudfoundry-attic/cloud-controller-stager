package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"time"

	"github.com/cloudfoundry-incubator/docker_app_lifecycle"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/routes"
	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
)

const (
	DockerLifecycleName         = "docker"
	DockerBuilderExecutablePath = "/tmp/docker_app_lifecycle/builder"
	DockerBuilderOutputPath     = "/tmp/docker-result/result.json"
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

func (backend *dockerBackend) BuildRecipe(stagingGuid string, request cc_messages.StagingRequestFromCC) (receptor.TaskCreateRequest, error) {
	logger := backend.logger.Session("build-recipe")
	logger.Info("staging-request", lager.Data{"Request": request})

	var lifecycleData cc_messages.DockerStagingData
	err := json.Unmarshal(*request.LifecycleData, &lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	err = backend.validateRequest(request, lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	compilerURL, err := backend.compilerDownloadURL()
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

	fileDescriptorLimit := uint64(request.FileDescriptors)

	//Run Smelter
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.RunAction{
				Path: DockerBuilderExecutablePath,
				Args: []string{"-outputMetadataJSONFilename", DockerBuilderOutputPath, "-dockerRef", lifecycleData.DockerImageUrl},
				Env:  request.Environment.BBSEnvironment(),
				ResourceLimits: models.ResourceLimits{
					Nofile: &fileDescriptorLimit,
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		),
	)

	annotationJson, _ := json.Marshal(cc_messages.StagingTaskAnnotation{
		Lifecycle: DockerLifecycleName,
	})

	task := receptor.TaskCreateRequest{
		TaskGuid:              stagingGuid,
		ResultFile:            DockerBuilderOutputPath,
		Domain:                backend.config.TaskDomain,
		RootFS:                models.PreloadedRootFS(request.Stack),
		MemoryMB:              request.MemoryMB,
		DiskMB:                request.DiskMB,
		Action:                models.Timeout(models.Serial(actions...), dockerTimeout(request, backend.logger)),
		CompletionCallbackURL: backend.config.CallbackURL(stagingGuid),
		LogGuid:               request.LogGuid,
		LogSource:             TaskLogSource,
		Annotation:            string(annotationJson),
		EgressRules:           request.EgressRules,
		Privileged:            false,
	}

	logger.Debug("staging-task-request", lager.Data{"TaskCreateRequest": task})

	return task, nil
}

func (backend *dockerBackend) BuildStagingResponse(taskResponse receptor.TaskResponse) (cc_messages.StagingResponseForCC, error) {
	var response cc_messages.StagingResponseForCC

	var annotation cc_messages.StagingTaskAnnotation
	err := json.Unmarshal([]byte(taskResponse.Annotation), &annotation)
	if err != nil {
		return cc_messages.StagingResponseForCC{}, err
	}

	if taskResponse.Failed {
		response.Error = backend.config.Sanitizer(taskResponse.FailureReason)
	} else {
		var result docker_app_lifecycle.StagingDockerResult
		err := json.Unmarshal([]byte(taskResponse.Result), &result)
		if err != nil {
			return cc_messages.StagingResponseForCC{}, err
		}

		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	return response, nil
}

func (backend *dockerBackend) compilerDownloadURL() (*url.URL, error) {
	lifecycleFilename := backend.config.Lifecycles["docker"]
	if lifecycleFilename == "" {
		return nil, ErrNoCompilerDefined
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

func (backend *dockerBackend) validateRequest(stagingRequest cc_messages.StagingRequestFromCC, dockerData cc_messages.DockerStagingData) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(dockerData.DockerImageUrl) == 0 {
		return ErrMissingDockerImageUrl
	}

	return nil
}

func dockerTimeout(request cc_messages.StagingRequestFromCC, logger lager.Logger) time.Duration {
	if request.Timeout > 0 {
		return time.Duration(request.Timeout) * time.Second
	} else {
		logger.Info("overriding requested timeout", lager.Data{
			"requested-timeout": request.Timeout,
			"default-timeout":   DefaultStagingTimeout,
			"app-id":            request.AppId,
		})
		return DefaultStagingTimeout
	}
}
