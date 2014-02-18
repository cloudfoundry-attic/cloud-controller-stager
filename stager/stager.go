package stager

import (
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"strings"
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

	buildpackDownloadActions := []models.ExecutorAction{}
	for _, buildpack := range request.AdminBuildpacks {
		buildpackDownloadActions = append(buildpackDownloadActions, models.ExecutorAction{
			models.DownloadAction{
				From:    buildpack.Url,
				To:      "/buildpacks/" + buildpack.Key,
				Extract: true,
			},
		})
	}

	err := stager.stagerBBS.DesireRunOnce(models.RunOnce{
		Guid:     strings.Join([]string{request.AppId, request.TaskId}, "-"),
		Stack:    request.Stack,
		ReplyTo:  replyTo,
		MemoryMB: request.MemoryMB,
		DiskMB:   request.DiskMB,
		Actions: append([]models.ExecutorAction{
			{
				models.DownloadAction{
					From:    compiler,
					To:      "/compiler",
					Extract: false,
				},
			},
			{
				models.DownloadAction{
					From:    request.DownloadUri,
					To:      "/app",
					Extract: true,
				},
			},
		}, buildpackDownloadActions...),
	})

	return err
}
