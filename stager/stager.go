package stager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/cloudfoundry/storeadapter"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/router"
	"github.com/cloudfoundry/gunk/urljoiner"
)

const (
	TaskDomain = "cf-app-staging"

	stagingRequestArrivedCounter = metric.Counter("StagingRequestsReceived")
)

type Config struct {
	Circuses           map[string]string
	DockerCircusPath   string
	MinMemoryMB        uint
	MinDiskMB          uint
	MinFileDescriptors uint64
}

type Stager interface {
	Stage(cc_messages.StagingRequestFromCC) error
}

type stager struct {
	stagerBBS bbs.StagerBBS
	logger    lager.Logger
	config    Config
}

func New(stagerBBS bbs.StagerBBS, logger lager.Logger, config Config) Stager {
	return &stager{
		stagerBBS: stagerBBS,
		logger:    logger,
		config:    config,
	}
}

var ErrNoFileServerPresent = errors.New("no available file server present")
var ErrNoCompilerDefined = errors.New("no compiler defined for requested stack")

func (stager *stager) Stage(request cc_messages.StagingRequestFromCC) error {
	stagingRequestArrivedCounter.Increment()

	fileServerURL, err := stager.stagerBBS.GetAvailableFileServer()
	if err != nil {
		return ErrNoFileServerPresent
	}

	compilerURL, err := stager.compilerDownloadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	buildpacksOrder := []string{}
	for _, buildpack := range request.Buildpacks {
		buildpacksOrder = append(buildpacksOrder, buildpack.Key)
	}

	tailorConfig := models.NewCircusTailorConfig(buildpacksOrder)

	actions := []models.ExecutorAction{}

	downloadActions := []models.ExecutorAction{}
	downloadNames := []string{}

	//Download tailor
	downloadActions = append(
		downloadActions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     compilerURL.String(),
					To:       path.Dir(tailorConfig.ExecutablePath),
					CacheKey: fmt.Sprintf("tailor-%s", request.Stack),
				},
			},
			"",
			"",
			"Failed to Download Tailor",
		),
	)

	//Download App Package
	downloadActions = append(
		downloadActions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From: request.AppBitsDownloadUri,
					To:   tailorConfig.AppDir(),
				},
			},
			"",
			"Downloaded App Package",
			"Failed to Download App Package",
		),
	)
	downloadNames = append(downloadNames, "app")

	//Download Buildpacks
	buildpackNames := []string{}
	for _, buildpack := range request.Buildpacks {
		downloadActions = append(
			downloadActions,
			models.EmitProgressFor(
				models.ExecutorAction{
					models.DownloadAction{
						From:     buildpack.Url,
						To:       tailorConfig.BuildpackPath(buildpack.Key),
						CacheKey: buildpack.Key,
					},
				},
				"",
				fmt.Sprintf("Downloaded Buildpack: %s", buildpack.Name),
				fmt.Sprintf("Failed to Download Buildpack: %s", buildpack.Name),
			),
		)
		buildpackNames = append(buildpackNames, buildpack.Name)
	}

	downloadNames = append(downloadNames, fmt.Sprintf("buildpacks (%s)", strings.Join(buildpackNames, ", ")))

	//Download Buildpack Artifacts Cache
	downloadURL, err := stager.buildArtifactsDownloadURL(request)
	if err != nil {
		return err
	}

	if downloadURL != nil {
		downloadActions = append(
			downloadActions,
			models.Try(
				models.EmitProgressFor(
					models.ExecutorAction{
						models.DownloadAction{
							From: downloadURL.String(),
							To:   tailorConfig.BuildArtifactsCacheDir(),
						},
					},
					"",
					"Downloaded Build Artifacts Cache",
					"No Build Artifacts Cache Found.  Proceeding...",
				),
			),
		)
		downloadNames = append(downloadNames, "artifacts cache")
	}

	downloadMsg := fmt.Sprintf("Fetching %s...", strings.Join(downloadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(downloadActions...), downloadMsg, "Fetching complete", "Fetching failed"))

	var fileDescriptorLimit *uint64
	if request.FileDescriptors != 0 {
		fd := max(uint64(request.FileDescriptors), stager.config.MinFileDescriptors)
		fileDescriptorLimit = &fd
	}

	//Run Tailor
	actions = append(
		actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.RunAction{
					Path:    tailorConfig.Path(),
					Args:    tailorConfig.Args(),
					Env:     request.Environment.BBSEnvironment(),
					Timeout: 15 * time.Minute,
					ResourceLimits: models.ResourceLimits{
						Nofile: fileDescriptorLimit,
					},
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		),
	)

	uploadActions := []models.ExecutorAction{}
	uploadNames := []string{}
	//Upload Droplet
	uploadURL, err := stager.dropletUploadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	uploadActions = append(
		uploadActions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.UploadAction{
					From: tailorConfig.OutputDroplet(), // get the droplet
					To:   uploadURL.String(),
				},
			},
			"",
			"Droplet Uploaded",
			"Failed to Upload Droplet",
		),
	)
	uploadNames = append(uploadNames, "droplet")

	//Upload Buildpack Artifacts Cache
	uploadURL, err = stager.buildArtifactsUploadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	uploadActions = append(uploadActions,
		models.Try(
			models.EmitProgressFor(
				models.ExecutorAction{
					models.UploadAction{
						From: tailorConfig.OutputBuildArtifactsCache(), // get the compressed build artifacts cache
						To:   uploadURL.String(),
					},
				},
				"",
				"Uploaded Build Artifacts Cache",
				"Failed to Upload Build Artifacts Cache.  Proceeding...",
			),
		),
	)
	uploadNames = append(uploadNames, "artifacts cache")

	uploadMsg := fmt.Sprintf("Uploading %s...", strings.Join(uploadNames, ", "))
	actions = append(actions, models.EmitProgressFor(models.Parallel(uploadActions...), uploadMsg, "Uploading complete", "Uploading failed"))

	annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
		AppId:  request.AppId,
		TaskId: request.TaskId,
	})

	task := models.Task{
		TaskGuid:   taskGuid(request),
		Domain:     TaskDomain,
		Stack:      request.Stack,
		ResultFile: tailorConfig.OutputMetadata(),
		MemoryMB:   int(max(uint64(request.MemoryMB), uint64(stager.config.MinMemoryMB))),
		DiskMB:     int(max(uint64(request.DiskMB), uint64(stager.config.MinDiskMB))),
		Actions:    actions,
		Log: models.LogConfig{
			Guid:       request.AppId,
			SourceName: "STG",
		},
		Annotation: string(annotationJson),
	}

	stager.logger.Info("desiring-task", lager.Data{"task": task})

	err = stager.stagerBBS.DesireTask(task)
	if err == storeadapter.ErrorKeyExists {
		err = nil
	}

	return err
}

func max(x, y uint64) uint64 {
	if x > y {
		return x
	} else {
		return y
	}
}

func taskGuid(request cc_messages.StagingRequestFromCC) string {
	return fmt.Sprintf("%s-%s", request.AppId, request.TaskId)
}

func (stager *stager) compilerDownloadURL(request cc_messages.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
	compilerPath, ok := stager.config.Circuses[request.Stack]
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

	staticRoute, ok := router.NewFileServerRoutes().RouteForHandler(router.FS_STATIC)
	if !ok {
		return nil, errors.New("couldn't generate the compiler download path")
	}

	urlString := urljoiner.Join(fileServerURL, staticRoute.Path, compilerPath)

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse compiler download URL: %s", err)
	}

	return url, nil
}

func (stager *stager) dropletUploadURL(request cc_messages.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
	staticRoute, ok := router.NewFileServerRoutes().RouteForHandler(router.FS_UPLOAD_DROPLET)
	if !ok {
		return nil, errors.New("couldn't generate the droplet upload path")
	}

	path, err := staticRoute.PathWithParams(map[string]string{
		"guid": request.AppId,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to build droplet upload URL: %s", err)
	}

	urlString := urljoiner.Join(fileServerURL, path)

	u, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse droplet upload URL: %s", err)
	}

	values := make(url.Values, 1)
	values.Add(models.CcDropletUploadUriKey, request.DropletUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (stager *stager) buildArtifactsUploadURL(request cc_messages.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
	staticRoute, ok := router.NewFileServerRoutes().RouteForHandler(router.FS_UPLOAD_BUILD_ARTIFACTS)
	if !ok {
		return nil, errors.New("couldn't generate the build artifacts cache upload path")
	}

	path, err := staticRoute.PathWithParams(map[string]string{
		"app_guid": request.AppId,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to build build artifacts cache upload URL: %s", err)
	}

	urlString := urljoiner.Join(fileServerURL, path)

	u, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse build artifacts cache upload URL: %s", err)
	}

	values := make(url.Values, 1)
	values.Add(models.CcBuildArtifactsUploadUriKey, request.BuildArtifactsCacheUploadUri)
	u.RawQuery = values.Encode()

	return u, nil
}

func (stager *stager) buildArtifactsDownloadURL(request cc_messages.StagingRequestFromCC) (*url.URL, error) {
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
