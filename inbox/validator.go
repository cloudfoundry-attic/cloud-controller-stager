package inbox

import (
	"errors"

	"github.com/cloudfoundry-incubator/stager/staging_messages"
)

var ErrMissingAppId = errors.New("missing app id")
var ErrMissingTaskId = errors.New("missing task id")
var ErrMissingAppBitsDownloadUri = errors.New("missing app bits download uri")

func ValidateRequest(stagingRequest staging_messages.StagingRequestFromCC) error {
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
