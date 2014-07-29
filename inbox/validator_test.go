package inbox_test

import (
	. "github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/staging_messages"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Validator", func() {
	var request staging_messages.StagingRequestFromCC
	var validator RequestValidator

	BeforeEach(func() {
		request = staging_messages.StagingRequestFromCC{
			AppId:              "hip",
			TaskId:             "hop",
			AppBitsDownloadUri: "http://example-uri.com/bunny",
			Stack:              "rabbit_hole",
			MemoryMB:           256,
			DiskMB:             1024,
			BuildArtifactsCacheDownloadUri: "http://example-uri.com/bunny-droppings",
		}

		validator = ValidateRequest
	})

	It("returns an error for a missing app id", func() {
		request.AppId = ""

		err := validator(request)
		Ω(err).Should(HaveOccurred())
		Ω(err.Error()).Should(Equal("missing app id"))
	})

	It("returns an error for a missing task id", func() {
		request.TaskId = ""

		err := validator(request)
		Ω(err).Should(HaveOccurred())
		Ω(err.Error()).Should(Equal("missing task id"))
	})

	It("returns an error for a missing app bits download uri", func() {
		request.AppBitsDownloadUri = ""

		err := validator(request)
		Ω(err).Should(HaveOccurred())
		Ω(err.Error()).Should(Equal("missing app bits download uri"))
	})
})
