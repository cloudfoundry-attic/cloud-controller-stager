package fake_stager

import (
	"errors"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
)

type FakeStager struct {
	TimesStageInvoked int
	StagingRequests   []models.StagingRequestFromCC
	AlwaysFail        bool //bringing shame and disgrace to its family and friends
}

func (stager *FakeStager) Stage(stagingRequest models.StagingRequestFromCC) error {
	stager.TimesStageInvoked++
	stager.StagingRequests = append(stager.StagingRequests, stagingRequest)

	if stager.AlwaysFail {
		return errors.New("The thingy broke :(")
	} else {
		return nil
	}
}
