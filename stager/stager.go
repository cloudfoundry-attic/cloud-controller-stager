package stager

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"time"

	"github.com/cloudfoundry/storeadapter"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/runtime-schema/router"
	"github.com/cloudfoundry/gunk/urljoiner"
)

type Stager interface {
	Stage(models.StagingRequestFromCC) error
}

type stager struct {
	stagerBBS bbs.StagerBBS
	compilers map[string]string
}

func New(stagerBBS bbs.StagerBBS, compilers map[string]string) Stager {
	return &stager{
		stagerBBS: stagerBBS,
		compilers: compilers,
	}
}

var ErrNoFileServerPresent = errors.New("no available file server present")
var ErrNoCompilerDefined = errors.New("no compiler defined for requested stack")

func (stager *stager) Stage(request models.StagingRequestFromCC) error {
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

	tailorConfig := models.NewLinuxCircusTailorConfig(buildpacksOrder)

	actions := []models.ExecutorAction{}

	//Download tailor
	actions = append(
		actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     compilerURL.String(),
					To:       path.Dir(tailorConfig.ExecutablePath),
					Extract:  true,
					CacheKey: fmt.Sprintf("tailor-%s", request.Stack),
				},
			},
			"",
			"",
			"Failed to Download Tailor",
		),
	)

	//Download App Package
	actions = append(
		actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:    request.AppBitsDownloadUri,
					To:      tailorConfig.AppDir(),
					Extract: true,
				},
			},
			"Downloading App Package",
			"Downloaded App Package",
			"Failed to Download App Package",
		),
	)

	//Download Buildpacks
	for _, buildpack := range request.Buildpacks {
		actions = append(
			actions,
			models.EmitProgressFor(
				models.ExecutorAction{
					models.DownloadAction{
						From:     buildpack.Url,
						To:       tailorConfig.BuildpackPath(buildpack.Key),
						Extract:  true,
						CacheKey: buildpack.Key,
					},
				},
				fmt.Sprintf("Downloading Buildpack: %s", buildpack.Name),
				fmt.Sprintf("Downloaded Buildpack: %s", buildpack.Name),
				fmt.Sprintf("Failed to Download Buildpack: %s", buildpack.Name),
			),
		)
	}

	//Download Buildpack Artifacts Cache
	downloadURL, err := stager.buildArtifactsDownloadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	if downloadURL != nil {
		actions = append(
			actions,
			models.Try(
				models.EmitProgressFor(
					models.ExecutorAction{
						models.DownloadAction{
							From:    downloadURL.String(),
							To:      tailorConfig.BuildArtifactsCacheDir(),
							Extract: true,
						},
					},
					"Downloading Build Artifacts Cache",
					"Downloaded Build Artifacts Cache",
					"No Build Artifacts Cache Found.  Proceeding...",
				),
			),
		)
	}

	var fileDescriptorLimit *uint64
	if request.FileDescriptors != 0 {
		fd := uint64(request.FileDescriptors)
		fileDescriptorLimit = &fd
	}

	//Run Smelter
	actions = append(
		actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.RunAction{
					Script:  tailorConfig.Script(),
					Env:     request.Environment,
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

	//Upload Droplet
	uploadURL, err := stager.dropletUploadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	actions = append(
		actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.UploadAction{
					From: tailorConfig.OutputDropletDir() + "/", // get the contents, not the directory itself
					To:   uploadURL.String(),
				},
			},
			"Uploading Droplet",
			"Droplet Uploaded",
			"Failed to Upload Droplet",
		),
	)

	//Upload Buildpack Artifacts Cache
	uploadURL, err = stager.buildArtifactsUploadURL(request, fileServerURL)
	if err != nil {
		return err
	}

	actions = append(actions,
		models.Try(
			models.EmitProgressFor(
				models.ExecutorAction{
					models.UploadAction{
						From:     tailorConfig.BuildArtifactsCacheDir() + "/", // get the contents, not the directory itself
						To:       uploadURL.String(),
						Compress: true,
					},
				},
				"Uploading Build Artifacts Cache",
				"Uploaded Build Artifacts Cache",
				"Failed to Upload Build Artifacts Cache.  Proceeding...",
			),
		),
	)

	//Fetch Result
	actions = append(actions,
		models.EmitProgressFor(
			models.ExecutorAction{
				models.FetchResultAction{
					File: tailorConfig.OutputMetadataPath(),
				},
			},
			"",
			"",
			"Failed to Fetch Detected Buildpack",
		),
	)

	annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
		AppId:  request.AppId,
		TaskId: request.TaskId,
	})

	//Go!
	_, err = stager.stagerBBS.DesireTask(models.Task{
		Type:     models.TaskTypeStaging,
		Guid:     taskGuid(request),
		Stack:    request.Stack,
		MemoryMB: request.MemoryMB,
		DiskMB:   request.DiskMB,
		Actions:  actions,
		Log: models.LogConfig{
			Guid:       request.AppId,
			SourceName: "STG",
		},
		Annotation: string(annotationJson),
	})
	if err == storeadapter.ErrorKeyExists {
		err = nil
	}

	return err
}

func taskGuid(request models.StagingRequestFromCC) string {
	return fmt.Sprintf("%s-%s", request.AppId, request.TaskId)
}

func (stager *stager) compilerDownloadURL(request models.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
	compilerPath, ok := stager.compilers[request.Stack]
	if !ok {
		return nil, ErrNoCompilerDefined
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

func (stager *stager) dropletUploadURL(request models.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
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

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse droplet upload URL: %s", err)
	}

	return url, nil
}

func (stager *stager) buildArtifactsUploadURL(request models.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {
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

	url, err := url.ParseRequestURI(urlString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse build artifacts cache upload URL: %s", err)
	}

	return url, nil
}

func (stager *stager) buildArtifactsDownloadURL(request models.StagingRequestFromCC, fileServerURL string) (*url.URL, error) {

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
