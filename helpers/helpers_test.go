package helpers_test

import (
	"github.com/cloudfoundry-incubator/stager/helpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Stager helpers", func() {

	Describe("BuildDockerStagingData", func() {

		It("builds the correct json", func() {
			lifecycleData, err := helpers.BuildDockerStagingData("cloudfoundry/diego-docker-app")
			Ω(err).ShouldNot(HaveOccurred())

			json := []byte(*lifecycleData)
			Ω(json).Should(MatchJSON(`{"docker_image":"cloudfoundry/diego-docker-app"}`))
		})
	})

})
