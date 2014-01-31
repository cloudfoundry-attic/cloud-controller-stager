package stager

import (
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"strings"
)

type Stager interface {
	Stage(StagingRequest, string) error
}

type stager struct {
	stagerBBS bbs.StagerBBS
}

func NewStager(stagerBBS bbs.StagerBBS) Stager {
	return &stager{
		stagerBBS: stagerBBS,
	}
}

func (stager *stager) Stage(request StagingRequest, replyTo string) error {
	err := stager.stagerBBS.DesireRunOnce(models.RunOnce{
		Guid:    strings.Join([]string{request.AppId, request.TaskId}, "-"),
		Stack:   request.Stack,
		ReplyTo: replyTo,
		Actions: []models.ExecutorAction{
			models.NewCopyAction(
				request.DownloadUri,
				"/app",
				true,
				false,
			),
		},
	})

	return err
}
