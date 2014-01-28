package fakestager

import (
	"errors"
	"github.com/pivotal-cf-experimental/stager/stager"
)

type FakeStager struct {
	TimesStageInvoked int
	StagingRequests   []stager.StagingRequest
	AlwaysFail        bool //bringing shame and disgrace to its family and friends
}

func (stager *FakeStager) Stage(stagingRequest stager.StagingRequest) error {
	stager.TimesStageInvoked++
	stager.StagingRequests = append(stager.StagingRequests, stagingRequest)
	if stager.AlwaysFail {
		return errors.New("The thingy broke :(")
	} else {
		return nil
	}
}
