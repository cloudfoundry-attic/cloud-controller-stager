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
	"github.com/cloudfoundry-incubator/runtime-schema/diego_errors"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/routes"
	"github.com/cloudfoundry-incubator/stager/helpers"
	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
)

const (
	DockerLifecycleName         = "docker"
	DockerBuilderExecutablePath = "/tmp/docker_app_lifecycle/builder"
	DockerBuilderOutputPath     = "/tmp/docker-result/result.json"
)

var ErrMissingDockerImageUrl = errors.New(diego_errors.MISSING_DOCKER_IMAGE_URL)
var ErrMissingDockerRegistry = errors.New(diego_errors.MISSING_DOCKER_REGISTRY)
var ErrMissingDockerCredentials = errors.New(diego_errors.MISSING_DOCKER_CREDENTIALS)

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

	cacheDockerImage := false
	for _, envVar := range request.Environment {
		if envVar.Name == "DIEGO_DOCKER_CACHE" && envVar.Value == "true" {
			cacheDockerImage = true
			break
		}
	}

	actions := []models.Action{}

	//Download builder
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.DownloadAction{
				From:     compilerURL.String(),
				To:       path.Dir(DockerBuilderExecutablePath),
				CacheKey: "docker-lifecycle",
			},
			"",
			"",
			"Failed to set up docker environment",
		),
	)

	runActionArguments := []string{"-outputMetadataJSONFilename", DockerBuilderOutputPath, "-dockerRef", lifecycleData.DockerImageUrl}
	runAs := "vcap"
	if cacheDockerImage {
		runAs = "root"
		registryServices, err := getDockerRegistryServices(backend.config.ConsulCluster, backend.logger)
		if err != nil {
			return receptor.TaskCreateRequest{}, err
		}
		registryRules := addDockerRegistryRules(request.EgressRules, registryServices)
		request.EgressRules = append(request.EgressRules, registryRules...)

		registryAddresses := strings.Join(buildDockerRegistryAddresses(registryServices), ",")

		runActionArguments = addDockerCachingArguments(runActionArguments, registryAddresses, backend.config.InsecureDockerRegistry, lifecycleData)
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
				User: runAs,
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
		RootFS:                models.PreloadedRootFS(backend.config.DockerStagingStack),
		MemoryMB:              request.MemoryMB,
		DiskMB:                request.DiskMB,
		Action:                models.Timeout(models.Serial(actions...), dockerTimeout(request, backend.logger)),
		CompletionCallbackURL: backend.config.CallbackURL(stagingGuid),
		LogGuid:               request.LogGuid,
		LogSource:             TaskLogSource,
		Annotation:            string(annotationJson),
		EgressRules:           request.EgressRules,
		Privileged:            cacheDockerImage,
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

		dockerLifecycleData, err := helpers.BuildDockerStagingData(result.DockerImage)
		if err != nil {
			return cc_messages.StagingResponseForCC{}, err
		}

		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
		response.LifecycleData = dockerLifecycleData
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

	credentialsPresent := (len(dockerData.DockerUser) + len(dockerData.DockerPassword) + len(dockerData.DockerEmail)) > 0
	if credentialsPresent && (len(dockerData.DockerUser) == 0 || len(dockerData.DockerPassword) == 0 || len(dockerData.DockerEmail) == 0) {
		return ErrMissingDockerCredentials
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

func getDockerRegistryServices(consulCluster string, backendLogger lager.Logger) ([]consulServiceInfo, error) {
	logger := backendLogger.Session("docker-registry-consul-services")

	response, err := http.Get(consulCluster + "/v1/catalog/service/docker-registry")
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

	if len(ips) == 0 {
		return nil, ErrMissingDockerRegistry
	}

	logger.Debug("docker-registry-consul-services", lager.Data{"ips": ips})

	return ips, nil
}

func addDockerCachingArguments(args []string, registryAddresses string, insecureRegistry bool, stagingData cc_messages.DockerStagingData) []string {
	args = append(args, "-cacheDockerImage")

	args = append(args, "-dockerRegistryAddresses", registryAddresses)
	if insecureRegistry {
		args = append(args, "-insecureDockerRegistries", registryAddresses)
	}

	if len(stagingData.DockerLoginServer) > 0 {
		args = append(args, "-dockerLoginServer", stagingData.DockerLoginServer)
	}
	if len(stagingData.DockerUser) > 0 {
		args = append(args, "-dockerUser", stagingData.DockerUser,
			"-dockerPassword", stagingData.DockerPassword,
			"-dockerEmail", stagingData.DockerEmail)
	}

	return args
}
