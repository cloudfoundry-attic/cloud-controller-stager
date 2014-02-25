package stager

import (
	"encoding/json"
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry/gunk/urljoiner"
	"strings"
	"time"
)

type Stager interface {
	Stage(StagingRequest, string) error
}

type stager struct {
	stagerBBS bbs.StagerBBS
	compilers map[string]string
}

func NewStager(stagerBBS bbs.StagerBBS, compilers map[string]string) Stager {
	return &stager{
		stagerBBS: stagerBBS,
		compilers: compilers,
	}
}

func (stager *stager) Stage(request StagingRequest, replyTo string) error {
	compilerPath, ok := stager.compilers[request.Stack]
	if !ok {
		return errors.New("No compiler defined for requested stack")
	}

	fileServerURL, err := stager.stagerBBS.GetAvailableFileServer()
	if err != nil {
		return errors.New("No available file server present")
	}

	compilerURL := urljoiner.Join(fileServerURL, compilerPath)
	actions := []models.ExecutorAction{}

	actions = append(actions, models.ExecutorAction{
		models.DownloadAction{
			From:    compilerURL,
			To:      "/tmp/compiler",
			Extract: true,
		},
	})

	actions = append(actions, models.ExecutorAction{
		models.DownloadAction{
			From:    request.DownloadUri,
			To:      "/app",
			Extract: true,
		},
	})

	buildpacksOrder := []string{}
	for _, buildpack := range request.AdminBuildpacks {
		actions = append(actions, models.ExecutorAction{
			models.DownloadAction{
				From:    buildpack.Url,
				To:      "/tmp/buildpacks/" + buildpack.Key,
				Extract: true,
			},
		})

		buildpacksOrder = append(buildpacksOrder, buildpack.Key)
	}
	buildpacksOrderJSON, _ := json.Marshal(buildpacksOrder)

	env := [][]string{
		{"APP_DIR", "/app"},
		{"OUTPUT_DIR", "/tmp/droplet"},
		{"BUILDPACKS_DIR", "/tmp/buildpacks"},
		{"BUILDPACK_ORDER", string(buildpacksOrderJSON)},
		{"CACHE_DIR", "/tmp/cache"},
	}
	env = append(request.Environment, env...)

	actions = append(actions, models.ExecutorAction{
		models.RunAction{
			Script:  "/tmp/compiler/run",
			Env:     env,
			Timeout: 15 * time.Minute,
		},
	})

	err = stager.stagerBBS.DesireRunOnce(models.RunOnce{
		Guid:     strings.Join([]string{request.AppId, request.TaskId}, "-"),
		Stack:    request.Stack,
		ReplyTo:  replyTo,
		MemoryMB: request.MemoryMB,
		DiskMB:   request.DiskMB,
		Actions:  actions,
		Log: models.LogConfig{
			Guid:       request.AppId,
			SourceName: "STG",
		},
	})

	return err
}
