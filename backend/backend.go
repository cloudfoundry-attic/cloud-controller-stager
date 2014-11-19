package backend

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

const (
	TaskLogSource         = "STG"
	DefaultStagingTimeout = 15 * time.Minute
)

type Backend interface {
	StagingRequestsNatsSubject() string
	StopStagingRequestsNatsSubject() string
	StagingRequestsReceivedCounter() metric.Counter
	StopStagingRequestsReceivedCounter() metric.Counter
	TaskDomain() string

	BuildRecipe(requestJson []byte) (receptor.TaskCreateRequest, error)
	BuildStagingResponse(receptor.TaskResponse) ([]byte, error)
	BuildStagingResponseFromRequestError(requestJson []byte, errorMessage string) ([]byte, error)

	StagingTaskGuid(requestJson []byte) (string, error)
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
	SkipCertVerify     bool
}

func max(x, y uint64) uint64 {
	if x > y {
		return x
	} else {
		return y
	}
}

func stagingTaskGuid(appId, taskId string) string {
	return fmt.Sprintf("%s-%s", appId, taskId)
}

func addTimeoutParamToURL(u url.URL, timeout time.Duration) *url.URL {
	query := u.Query()
	query.Set(models.CcTimeoutKey, fmt.Sprintf("%.0f", timeout.Seconds()))
	u.RawQuery = query.Encode()
	return &u
}
