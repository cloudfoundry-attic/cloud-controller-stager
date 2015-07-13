package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/cloudfoundry-incubator/buildpack_app_lifecycle"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/routes"
	"github.com/cloudfoundry/gunk/urljoiner"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/rata"
)

const (
	TraditionalLifecycleName = "buildpack"
	StagingTaskCpuWeight     = uint(50)

	DefaultLANG = "en_US.UTF-8"
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

func (backend *traditionalBackend) BuildRecipe(stagingGuid string, request cc_messages.StagingRequestFromCC) (receptor.TaskCreateRequest, error) {
	logger := backend.logger.Session("build-recipe", lager.Data{"app-id": request.AppId, "staging-guid": stagingGuid})
	logger.Info("staging-request")

	if request.LifecycleData == nil {
		return receptor.TaskCreateRequest{}, ErrMissingLifecycleData
	}

	var lifecycleData cc_messages.BuildpackStagingData
	err := json.Unmarshal(*request.LifecycleData, &lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	err = backend.validateRequest(request, lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	compilerURL, err := backend.compilerDownloadURL(request, lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	buildpacksOrder := []string{}
	for _, buildpack := range lifecycleData.Buildpacks {
		buildpacksOrder = append(buildpacksOrder, buildpack.Key)
	}

	skipDetect := len(lifecycleData.Buildpacks) == 1 && lifecycleData.Buildpacks[0].SkipDetect

	builderConfig := buildpack_app_lifecycle.NewLifecycleBuilderConfig(buildpacksOrder, skipDetect, backend.config.SkipCertVerify)

	timeout := traditionalTimeout(request, backend.logger)

	actions := []models.Action{}

	//Download app package
	appDownloadAction := &models.DownloadAction{
		Artifact: "app package",
		From:     lifecycleData.AppBitsDownloadUri,
		To:       builderConfig.BuildDir(),
		User:     "vcap",
	}

	actions = append(actions, appDownloadAction)

	downloadActions := []models.Action{}
	downloadNames := []string{}

	//Download builder
	downloadActions = append(
		downloadActions,
		models.EmitProgressFor(
			&models.DownloadAction{
				From:     compilerURL.String(),
				To:       path.Dir(builderConfig.ExecutablePath),
				CacheKey: fmt.Sprintf("buildpack-%s-lifecycle", lifecycleData.Stack),
				User:     "vcap",
			},
			"",
			"",
			"Failed to set up staging environment",
		),
	)

	//Download buildpacks
	buildpackNames := []string{}
	downloadMsgPrefix := ""
	if !skipDetect {
		downloadMsgPrefix = "No buildpack specified; fetching standard buildpacks to detect and build your application.\n"
	}
	for _, buildpack := range lifecycleData.Buildpacks {
		if buildpack.Name == cc_messages.CUSTOM_BUILDPACK {
			buildpackNames = append(buildpackNames, buildpack.Url)
		} else {
			buildpackNames = append(buildpackNames, buildpack.Name)
			downloadActions = append(
				downloadActions,
				&models.DownloadAction{
					Artifact: buildpack.Name,
					From:     buildpack.Url,
					To:       builderConfig.BuildpackPath(buildpack.Key),
					CacheKey: buildpack.Key,
					User:     "vcap",
				},
			)
		}
	}

	downloadNames = append(downloadNames, fmt.Sprintf("buildpacks (%s)", strings.Join(buildpackNames, ", ")))

	//Download buildpack artifacts cache
	downloadURL, err := backend.buildArtifactsDownloadURL(lifecycleData)
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
					To:       builderConfig.BuildArtifactsCacheDir(),
					User:     "vcap",
				},
			),
		)
		downloadNames = append(downloadNames, "build artifacts cache")
	}

	downloadMsg := downloadMsgPrefix + fmt.Sprintf("Downloading %s...", strings.Join(downloadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(downloadActions...), downloadMsg, "Downloaded buildpacks", "Downloading buildpacks failed"))

	fileDescriptorLimit := uint64(request.FileDescriptors)

	//Run Builder
	actions = append(
		actions,
		models.EmitProgressFor(
			&models.RunAction{
				User: "vcap",
				Path: builderConfig.Path(),
				Args: builderConfig.Args(),
				Env:  request.Environment.BBSEnvironment(),
				ResourceLimits: models.ResourceLimits{
					Nofile: &fileDescriptorLimit,
				},
			},
			"Staging...",
			"Staging complete",
			"Staging failed",
		),
	)

	//Upload Droplet
	uploadActions := []models.Action{}
	uploadNames := []string{}
	uploadURL, err := backend.dropletUploadURL(request, lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	uploadActions = append(
		uploadActions,
		&models.UploadAction{
			Artifact: "droplet",
			From:     builderConfig.OutputDroplet(), // get the droplet
			To:       addTimeoutParamToURL(*uploadURL, timeout).String(),
			User:     "vcap",
		},
	)
	uploadNames = append(uploadNames, "droplet")

	//Upload Buildpack Artifacts Cache
	uploadURL, err = backend.buildArtifactsUploadURL(request, lifecycleData)
	if err != nil {
		return receptor.TaskCreateRequest{}, err
	}

	uploadActions = append(uploadActions,
		models.Try(
			&models.UploadAction{
				Artifact: "build artifacts cache",
				From:     builderConfig.OutputBuildArtifactsCache(), // get the compressed build artifacts cache
				To:       addTimeoutParamToURL(*uploadURL, timeout).String(),
				User:     "vcap",
			},
		),
	)
	uploadNames = append(uploadNames, "build artifacts cache")

	uploadMsg := fmt.Sprintf("Uploading %s...", strings.Join(uploadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(uploadActions...), uploadMsg, "Uploading complete", "Uploading failed"))

	annotationJson, _ := json.Marshal(cc_messages.StagingTaskAnnotation{
		Lifecycle: TraditionalLifecycleName,
	})

	task := receptor.TaskCreateRequest{
		TaskGuid:              stagingGuid,
		Domain:                backend.config.TaskDomain,
		RootFS:                models.PreloadedRootFS(lifecycleData.Stack),
		ResultFile:            builderConfig.OutputMetadata(),
		MemoryMB:              request.MemoryMB,
		DiskMB:                request.DiskMB + 1024, // TEMPORARY FIX FOR GARDEN-LINUX
		CPUWeight:             StagingTaskCpuWeight,
		Action:                models.Timeout(models.Serial(actions...), timeout),
		LogGuid:               request.LogGuid,
		LogSource:             TaskLogSource,
		CompletionCallbackURL: backend.config.CallbackURL(stagingGuid),
		EgressRules:           request.EgressRules,
		Annotation:            string(annotationJson),
		Privileged:            true,
		EnvironmentVariables:  []receptor.EnvironmentVariable{{"LANG", DefaultLANG}},
	}

	logger.Debug("staging-task-request")

	return task, nil
}

func (backend *traditionalBackend) BuildStagingResponse(taskResponse receptor.TaskResponse) (cc_messages.StagingResponseForCC, error) {
	var response cc_messages.StagingResponseForCC

	var annotation cc_messages.StagingTaskAnnotation
	err := json.Unmarshal([]byte(taskResponse.Annotation), &annotation)
	if err != nil {
		return cc_messages.StagingResponseForCC{}, err
	}

	if taskResponse.Failed {
		response.Error = backend.config.Sanitizer(taskResponse.FailureReason)
	} else {
		var result buildpack_app_lifecycle.StagingResult
		err := json.Unmarshal([]byte(taskResponse.Result), &result)
		if err != nil {
			return cc_messages.StagingResponseForCC{}, err
		}

		buildpackResponse := cc_messages.BuildpackStagingResponse{
			BuildpackKey:      result.BuildpackKey,
			DetectedBuildpack: result.DetectedBuildpack,
		}

		lifecycleDataJSON, err := json.Marshal(buildpackResponse)
		if err != nil {
			return cc_messages.StagingResponseForCC{}, err
		}
		lifecycleData := json.RawMessage(lifecycleDataJSON)

		response.ExecutionMetadata = result.ExecutionMetadata
		response.DetectedStartCommand = result.DetectedStartCommand
		response.LifecycleData = &lifecycleData
	}

	return response, nil
}

func (backend *traditionalBackend) compilerDownloadURL(request cc_messages.StagingRequestFromCC, buildpackData cc_messages.BuildpackStagingData) (*url.URL, error) {
	compilerPath, ok := backend.config.Lifecycles[request.Lifecycle+"/"+buildpackData.Stack]
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

func (backend *traditionalBackend) dropletUploadURL(request cc_messages.StagingRequestFromCC, buildpackData cc_messages.BuildpackStagingData) (*url.URL, error) {
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
	values.Add(models.CcDropletUploadUriKey, buildpackData.DropletUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (backend *traditionalBackend) buildArtifactsUploadURL(request cc_messages.StagingRequestFromCC, buildpackData cc_messages.BuildpackStagingData) (*url.URL, error) {
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
	values.Add(models.CcBuildArtifactsUploadUriKey, buildpackData.BuildArtifactsCacheUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (backend *traditionalBackend) buildArtifactsDownloadURL(buildpackData cc_messages.BuildpackStagingData) (*url.URL, error) {
	urlString := buildpackData.BuildArtifactsCacheDownloadUri
	if urlString == "" {
		return nil, nil
	}

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse build artifacts cache download URL: %s", err)
	}

	return url, nil
}

func (backend *traditionalBackend) validateRequest(stagingRequest cc_messages.StagingRequestFromCC, buildpackData cc_messages.BuildpackStagingData) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(buildpackData.AppBitsDownloadUri) == 0 {
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
		})
		return DefaultStagingTimeout
	}
}
