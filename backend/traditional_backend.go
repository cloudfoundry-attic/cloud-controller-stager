package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/routes"
	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
)

const (
	TraditionalTaskDomain                         = "cf-app-staging"
	TraditionalStagingRequestsNatsSubject         = "diego.staging.start"
	TraditionalStagingRequestsReceivedCounter     = metric.Counter("TraditionalStagingRequestsReceived")
	TraditionalStopStagingRequestsNatsSubject     = "diego.staging.stop"
	TraditionalStopStagingRequestsReceivedCounter = metric.Counter("TraditionalStopStagingRequestsReceived")
	StagingTaskCpuWeight                          = uint(50)
)

type traditionalBackend struct {
	config Config
	logger lager.Logger
}

func NewTraditionalBackend(config Config, logger lager.Logger) Backend {
	return &traditionalBackend{
		config: config,
		logger: logger.Session("traditional"),
	}
}

func (backend *traditionalBackend) StagingRequestsNatsSubject() string {
	return TraditionalStagingRequestsNatsSubject
}

func (backend *traditionalBackend) StagingRequestsReceivedCounter() metric.Counter {
	return TraditionalStagingRequestsReceivedCounter
}

func (backend *traditionalBackend) StopStagingRequestsNatsSubject() string {
	return TraditionalStopStagingRequestsNatsSubject
}

func (backend *traditionalBackend) StopStagingRequestsReceivedCounter() metric.Counter {
	return TraditionalStopStagingRequestsReceivedCounter
}

func (backend *traditionalBackend) TaskDomain() string {
	return TraditionalTaskDomain
}

func (backend *traditionalBackend) BuildRecipe(requestJson []byte) (receptor.TaskCreateRequest, error) {
	logger := backend.logger.Session("build-recipe")

	var request cc_messages.StagingRequestFromCC
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

	buildpacksOrder := []string{}
	for _, buildpack := range request.Buildpacks {
		buildpacksOrder = append(buildpacksOrder, buildpack.Key)
	}

	tailorConfig := models.NewCircusTailorConfig(buildpacksOrder, backend.config.SkipCertVerify)

	timeout := traditionalTimeout(request, backend.logger)

	actions := []models.Action{}

	//Download app package
	appDownloadAction := &models.DownloadAction{
		Artifact: "app package",
		From:     request.AppBitsDownloadUri,
		To:       tailorConfig.BuildDir(),
	}

	actions = append(actions, appDownloadAction)

	downloadActions := []models.Action{}
	downloadNames := []string{}

	//Download tailor
	downloadActions = append(
		downloadActions,
		models.EmitProgressFor(
			&models.DownloadAction{
				From:     compilerURL.String(),
				To:       path.Dir(tailorConfig.ExecutablePath),
				CacheKey: fmt.Sprintf("tailor-%s", request.Stack),
			},
			"",
			"",
			"Failed to set up staging environment",
		),
	)

	//Download buildpacks
	buildpackNames := []string{}
	downloadMsgPrefix := ""
	if len(request.Buildpacks) > 1 {
		downloadMsgPrefix = "No buildpack specified; fetching standard buildpacks to detect and build your application.\n"
	}
	for _, buildpack := range request.Buildpacks {
		if buildpack.Name == cc_messages.CUSTOM_BUILDPACK {
			buildpackNames = append(buildpackNames, buildpack.Url)
		} else {
			buildpackNames = append(buildpackNames, buildpack.Name)
			downloadActions = append(
				downloadActions,
				&models.DownloadAction{
					Artifact: buildpack.Name,
					From:     buildpack.Url,
					To:       tailorConfig.BuildpackPath(buildpack.Key),
					CacheKey: buildpack.Key,
				},
			)
		}
	}

	downloadNames = append(downloadNames, fmt.Sprintf("buildpacks (%s)", strings.Join(buildpackNames, ", ")))

	//Download buildpack artifacts cache
	downloadURL, err := backend.buildArtifactsDownloadURL(request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	if downloadURL != nil {
		downloadActions = append(
			downloadActions,
			models.Try(
				&models.DownloadAction{
					Artifact: "build artifacts cache",
					From:     downloadURL.String(),
					To:       tailorConfig.BuildArtifactsCacheDir(),
				},
			),
		)
		downloadNames = append(downloadNames, "build artifacts cache")
	}

	downloadMsg := downloadMsgPrefix + fmt.Sprintf("Downloading %s...", strings.Join(downloadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(downloadActions...), downloadMsg, "Downloaded buildpacks", "Downloading buildpacks failed"))

	var fileDescriptorLimit *uint64
	if request.FileDescriptors != 0 {
		fd := max(uint64(request.FileDescriptors), backend.config.MinFileDescriptors)
		fileDescriptorLimit = &fd
	}

	//Run Tailor
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.RunAction{
				Path: tailorConfig.Path(),
				Args: tailorConfig.Args(),
				Env:  request.Environment.BBSEnvironment(),
				ResourceLimits: models.ResourceLimits{
					Nofile: fileDescriptorLimit,
				},
			},
			"Staging...",
			"Staging complete",
			"Staging failed",
		),
	)

	uploadActions := []models.Action{}
	uploadNames := []string{}
	//Upload Droplet
	uploadURL, err := backend.dropletUploadURL(request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	uploadActions = append(
		uploadActions,
		&models.UploadAction{
			Artifact: "droplet",
			From:     tailorConfig.OutputDroplet(), // get the droplet
			To:       addTimeoutParamToURL(*uploadURL, timeout).String(),
		},
	)
	uploadNames = append(uploadNames, "droplet")

	//Upload Buildpack Artifacts Cache
	uploadURL, err = backend.buildArtifactsUploadURL(request)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	uploadActions = append(uploadActions,
		models.Try(
			&models.UploadAction{
				Artifact: "build artifacts cache",
				From:     tailorConfig.OutputBuildArtifactsCache(), // get the compressed build artifacts cache
				To:       addTimeoutParamToURL(*uploadURL, timeout).String(),
			},
		),
	)
	uploadNames = append(uploadNames, "build artifacts cache")

	uploadMsg := fmt.Sprintf("Uploading %s...", strings.Join(uploadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(uploadActions...), uploadMsg, "Uploading complete", "Uploading failed"))

	annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
		AppId:  request.AppId,
		TaskId: request.TaskId,
	})

	task := receptor.TaskCreateRequest{
		TaskGuid:              backend.taskGuid(request),
		Domain:                TraditionalTaskDomain,
		Stack:                 request.Stack,
		ResultFile:            tailorConfig.OutputMetadata(),
		MemoryMB:              int(max(uint64(request.MemoryMB), uint64(backend.config.MinMemoryMB))),
		DiskMB:                int(max(uint64(request.DiskMB), uint64(backend.config.MinDiskMB))),
		CPUWeight:             StagingTaskCpuWeight,
		Action:                models.Timeout(models.Serial(actions...), timeout),
		LogGuid:               request.AppId,
		LogSource:             TaskLogSource,
		CompletionCallbackURL: backend.config.CallbackURL,
		Annotation:            string(annotationJson),
	}

	logger.Debug("staging-task-request", lager.Data{"TaskCreateRequest": task})

	return task, nil
}

func (backend *traditionalBackend) BuildStagingResponseFromRequestError(requestJson []byte, errorMessage string) ([]byte, error) {
	request := cc_messages.StagingRequestFromCC{}

	err := json.Unmarshal(requestJson, &request)
	if err != nil {
		return nil, err
	}

	response := cc_messages.StagingResponseForCC{
		AppId:  request.AppId,
		TaskId: request.TaskId,
		Error:  backend.config.Sanitizer(errorMessage),
	}

	return json.Marshal(response)
}

func (backend *traditionalBackend) BuildStagingResponse(taskResponse receptor.TaskResponse) ([]byte, error) {
	var response cc_messages.StagingResponseForCC

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
		var result models.StagingResult
		err := json.Unmarshal([]byte(taskResponse.Result), &result)
		if err != nil {
			return nil, err
		}

		response.BuildpackKey = result.BuildpackKey
		response.DetectedBuildpack = result.DetectedBuildpack
		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
	}

	return json.Marshal(response)
}

func (backend *traditionalBackend) StagingTaskGuid(requestJson []byte) (string, error) {
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

func (backend *traditionalBackend) taskGuid(request cc_messages.StagingRequestFromCC) string {
	return stagingTaskGuid(request.AppId, request.TaskId)
}

func (backend *traditionalBackend) compilerDownloadURL(request cc_messages.StagingRequestFromCC) (*url.URL, error) {
	compilerPath, ok := backend.config.Circuses[request.Stack]
	if !ok {
		return nil, ErrNoCompilerDefined
	}

	parsed, err := url.Parse(compilerPath)
	if err != nil {
		return nil, errors.New("couldn't parse compiler URL")
	}

	switch parsed.Scheme {
	case "http", "https":
		return parsed, nil
	case "":
		break
	default:
		return nil, errors.New("wTF")
	}

	staticPath, err := routes.FileServerRoutes.CreatePathForRoute(routes.FS_STATIC, nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't generate the compiler download path: %s", err)
	}

	urlString := urljoiner.Join(backend.config.FileServerURL, staticPath, compilerPath)

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse compiler download URL: %s", err)
	}

	return url, nil
}

func (backend *traditionalBackend) dropletUploadURL(request cc_messages.StagingRequestFromCC) (*url.URL, error) {
	path, err := routes.FileServerRoutes.CreatePathForRoute(routes.FS_UPLOAD_DROPLET, rata.Params{
		"guid": request.AppId,
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't generate droplet upload URL: %s", err)
	}

	urlString := urljoiner.Join(backend.config.FileServerURL, path)

	u, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse droplet upload URL: %s", err)
	}

	values := make(url.Values, 1)
	values.Add(models.CcDropletUploadUriKey, request.DropletUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (backend *traditionalBackend) buildArtifactsUploadURL(request cc_messages.StagingRequestFromCC) (*url.URL, error) {
	path, err := routes.FileServerRoutes.CreatePathForRoute(routes.FS_UPLOAD_BUILD_ARTIFACTS, rata.Params{
		"app_guid": request.AppId,
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't generate build artifacts cache upload URL: %s", err)
	}

	urlString := urljoiner.Join(backend.config.FileServerURL, path)

	u, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse build artifacts cache upload URL: %s", err)
	}

	values := make(url.Values, 1)
	values.Add(models.CcBuildArtifactsUploadUriKey, request.BuildArtifactsCacheUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (backend *traditionalBackend) buildArtifactsDownloadURL(request cc_messages.StagingRequestFromCC) (*url.URL, error) {
	urlString := request.BuildArtifactsCacheDownloadUri
	if urlString == "" {
		return nil, nil
	}

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse build artifacts cache download URL: %s", err)
	}

	return url, nil
}

func (backend *traditionalBackend) validateRequest(stagingRequest cc_messages.StagingRequestFromCC) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(stagingRequest.TaskId) == 0 {
		return ErrMissingTaskId
	}

	if len(stagingRequest.AppBitsDownloadUri) == 0 {
		return ErrMissingAppBitsDownloadUri
	}

	return nil
}

func traditionalTimeout(request cc_messages.StagingRequestFromCC, logger lager.Logger) time.Duration {
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
