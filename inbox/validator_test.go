package inbox_test

import (
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/inbox"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Validator", func() {
	var validator RequestValidator

	BeforeEach(func() {
		validator = ValidateRequest
	})

	It("returns an error for a missing app id", func() {
		err := validator(models.StagingRequestFromCC{
			AppId:              "",
			TaskId:             "hop",
			AppBitsDownloadUri: "http://example-uri.com/bunny",
			Stack:              "rabbit_hole",
			MemoryMB:           256,
			DiskMB:             1024,
		})

		Ω(err).Should(HaveOccurred())
		Ω(err.Error()).Should(Equal("missing app id"))
	})

	It("returns an error for a missing task id", func() {
		err := validator(models.StagingRequestFromCC{
			AppId:              "hip",
			TaskId:             "",
			AppBitsDownloadUri: "http://example-uri.com/bunny",
			Stack:              "rabbit_hole",
			MemoryMB:           256,
			DiskMB:             1024,
		})

		Ω(err).Should(HaveOccurred())
		Ω(err.Error()).Should(Equal("missing task id"))
	})
})
