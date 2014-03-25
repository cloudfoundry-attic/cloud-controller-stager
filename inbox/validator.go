package inbox

import (
	"errors"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

var ErrMissingAppId = errors.New("missing app id")
var ErrMissingTaskId = errors.New("missing task id")

func ValidateRequest(stagingRequest models.StagingRequestFromCC) error {
	if len(stagingRequest.AppId) == 0 {
		return ErrMissingAppId
	}

	if len(stagingRequest.TaskId) == 0 {
		return ErrMissingTaskId
	}
	return nil
}
