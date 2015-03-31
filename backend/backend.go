package backend

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/diego_errors"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

const (
	TaskLogSource         = "STG"
	DefaultStagingTimeout = 15 * time.Minute

	StagingTaskDomain = "cf-app-staging"
)

type FailureReasonSanitizer func(string) *cc_messages.StagingError

//go:generate counterfeiter -o fake_backend/fake_backend.go . Backend
type Backend interface {
	BuildRecipe(stagingGuid string, request cc_messages.StagingRequestFromCC) (receptor.TaskCreateRequest, error)
	BuildStagingResponse(receptor.TaskResponse) (cc_messages.StagingResponseForCC, error)
}

var ErrNoCompilerDefined = errors.New(diego_errors.NO_COMPILER_DEFINED_MESSAGE)
var ErrMissingAppId = errors.New(diego_errors.MISSING_APP_ID_MESSAGE)
var ErrMissingAppBitsDownloadUri = errors.New(diego_errors.MISSING_APP_BITS_DOWNLOAD_URI_MESSAGE)
var ErrMissingLifecycleData = errors.New(diego_errors.MISSING_LIFECYCLE_DATA_MESSAGE)

type Config struct {
	TaskDomain         string
	StagerURL          string
	FileServerURL      string
	Lifecycles         map[string]string
	SkipCertVerify     bool
	Sanitizer          FailureReasonSanitizer
	DockerStagingStack string
}

func (c Config) CallbackURL(stagingGuid string) string {
	return fmt.Sprintf("%s/v1/staging/%s/completed", c.StagerURL, stagingGuid)
}

func max(x, y uint64) uint64 {
	if x > y {
		return x
	} else {
		return y
	}
}

func addTimeoutParamToURL(u url.URL, timeout time.Duration) *url.URL {
	query := u.Query()
	query.Set(models.CcTimeoutKey, fmt.Sprintf("%.0f", timeout.Seconds()))
	u.RawQuery = query.Encode()
	return &u
}
