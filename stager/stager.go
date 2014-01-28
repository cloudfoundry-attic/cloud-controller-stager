package stager

import (
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"strings"
)

type Stager interface {
	Stage(StagingRequest) error
}

type stager struct {
	stagerBBS bbs.StagerBBS
}

func NewStager(stagerBBS bbs.StagerBBS) Stager {
	return &stager{
		stagerBBS: stagerBBS,
	}
}

func (stager *stager) Stage(request StagingRequest) error {
	err := stager.stagerBBS.DesireRunOnce(models.RunOnce{
		Guid: strings.Join([]string{request.AppId, request.TaskId}, "-"),
	})
	return err
}
