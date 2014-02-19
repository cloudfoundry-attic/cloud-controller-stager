package stager

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
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
	compiler, ok := stager.compilers[request.Stack]

	if !ok {
		return errors.New("No compiler defined for requested stack")
	}

	actions := []models.ExecutorAction{}

	actions = append(actions, models.ExecutorAction{
		models.DownloadAction{
			From:    compiler,
			To:      "/tmp/compiler",
			Extract: true,
		},
	})

	actions = append(actions, models.ExecutorAction{
		models.DownloadAction{
			From:    request.DownloadUri,
			To:      "/tmp/app",
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

	actions = append(actions, models.ExecutorAction{
		models.RunAction{
			Script: "/tmp/compiler/run",
			Env: map[string]string{
				"APP_DIR":         "/tmp/app",
				"OUTPUT_DIR":      "/tmp/droplet",
				"BUILDPACKS_DIR":  "/tmp/buildpacks",
				"BUILDPACK_ORDER": string(buildpacksOrderJSON),
				"CACHE_DIR":       "/tmp/cache",
				"MEMORY_LIMIT":    fmt.Sprintf("%dm", request.MemoryMB),
			},
			Timeout: 15 * time.Minute,
		},
	})

	err := stager.stagerBBS.DesireRunOnce(models.RunOnce{
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
