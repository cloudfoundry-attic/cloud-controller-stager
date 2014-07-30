package fake_stager

import (
	"errors"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
)

type FakeStager struct {
	TimesStageInvoked int
	StagingRequests   []cc_messages.StagingRequestFromCC
	AlwaysFail        bool //bringing shame and disgrace to its family and friends
}

func (stager *FakeStager) Stage(stagingRequest cc_messages.StagingRequestFromCC) error {
	stager.TimesStageInvoked++
	stager.StagingRequests = append(stager.StagingRequests, stagingRequest)

	if stager.AlwaysFail {
		return errors.New("The thingy broke :(")
	} else {
		return nil
	}
}
