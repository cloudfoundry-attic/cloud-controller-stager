package inbox

import (
	"errors"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

var ErrMissingAppId = errors.New("missing app id")
var ErrMissingTaskId = errors.New("missing task id")
var ErrMissingAppBitsDownloadUri = errors.New("missing app bits download uri")
var ErrMissingBuildArtifactsCacheDownloadUri = errors.New("missing build artifacts cache download uri")
var ErrMissingBuildArtifactsCacheUploadUri = errors.New("missing build artifacts cache upload uri")

func ValidateRequest(stagingRequest models.StagingRequestFromCC) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(stagingRequest.TaskId) == 0 {
		return ErrMissingTaskId
	}

	if len(stagingRequest.AppBitsDownloadUri) == 0 {
		return ErrMissingAppBitsDownloadUri
	}

	if len(stagingRequest.BuildArtifactsCacheDownloadUri) == 0 {
		return ErrMissingBuildArtifactsCacheDownloadUri
	}

	if len(stagingRequest.BuildArtifactsCacheUploadUri) == 0 {
		return ErrMissingBuildArtifactsCacheUploadUri
	}

	return nil
}
