package backend_test

import (
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func actionsFromDesiredTask(desiredTask receptor.TaskCreateRequest) []models.Action {
	timeoutAction := desiredTask.Action
	Expect(timeoutAction).To(BeAssignableToTypeOf(&models.TimeoutAction{}))

	serialAction := timeoutAction.(*models.TimeoutAction).Action
	Expect(serialAction).To(BeAssignableToTypeOf(&models.SerialAction{}))

	return serialAction.(*models.SerialAction).Actions
}

func TestBackend(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backend Suite")
}
