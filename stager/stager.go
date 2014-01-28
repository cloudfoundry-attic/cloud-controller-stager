package stager

type Stager interface {
	Stage(StagingRequest) error
}

type stager struct{}

func NewStager() Stager {
	return &stager{}
}

func (stager *stager) Stage(request StagingRequest) error {
	return nil
}
