package fake_stager_docker

import (
	"errors"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
)

type FakeStagerDocker struct {
	TimesStageInvoked int
	StagingRequests   []cc_messages.DockerStagingRequestFromCC
	AlwaysFail        bool //bringing shame and disgrace to its family and friends
}

func (stager *FakeStagerDocker) Stage(stagingRequest cc_messages.DockerStagingRequestFromCC) error {
	stager.TimesStageInvoked++
	stager.StagingRequests = append(stager.StagingRequests, stagingRequest)

	if stager.AlwaysFail {
		return errors.New("The thingy broke :(")
	} else {
		return nil
	}
}
