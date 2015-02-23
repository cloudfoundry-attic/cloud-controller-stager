package backend

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/diego_errors"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

const (
	TaskLogSource         = "STG"
	DefaultStagingTimeout = 15 * time.Minute
)

type FailureReasonSanitizer func(string) *cc_messages.StagingError

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

var ErrNoCompilerDefined = errors.New(diego_errors.NO_COMPILER_DEFINED_MESSAGE)
var ErrMissingAppId = errors.New(diego_errors.MISSING_APP_ID_MESSAGE)
var ErrMissingTaskId = errors.New(diego_errors.MISSING_TASK_ID_MESSAGE)
var ErrMissingAppBitsDownloadUri = errors.New(diego_errors.MISSING_APP_BITS_DOWNLOAD_URI_MESSAGE)

type DockerRegistry struct {
	URL      string
	Insecure bool
}

type Config struct {
	CallbackURL         string
	FileServerURL       string
	Lifecycles          map[string]string
	DockerLifecyclePath string
	DockerRegistry      *DockerRegistry
	ConsulAgentURL      string
	SkipCertVerify      bool
	Sanitizer           FailureReasonSanitizer
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
