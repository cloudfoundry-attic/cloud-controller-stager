package backend

import (
	"errors"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
)

const (
	TaskLogSource    = "CELL"
	StagingLogSource = "STAGING"
)

type Backend interface {
	StagingRequestsNatsSubject() string
	StagingRequestsReceivedCounter() metric.Counter
	TaskDomain() string

	BuildRecipe(requestJson []byte) (receptor.TaskCreateRequest, error)
	BuildStagingResponse(receptor.TaskResponse) ([]byte, error)
	BuildStagingResponseFromRequestError(requestJson []byte, errorMessage string) ([]byte, error)
}

var ErrNoCompilerDefined = errors.New("no compiler defined for requested stack")
var ErrMissingAppId = errors.New("missing app id")
var ErrMissingTaskId = errors.New("missing task id")
var ErrMissingAppBitsDownloadUri = errors.New("missing app bits download uri")

type Config struct {
	CallbackURL        string
	FileServerURL      string
	Circuses           map[string]string
	DockerCircusPath   string
	MinMemoryMB        uint
	MinDiskMB          uint
	MinFileDescriptors uint64
}

func max(x, y uint64) uint64 {
	if x > y {
		return x
	} else {
		return y
	}
}
