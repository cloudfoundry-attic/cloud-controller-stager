package inbox_test

import (
	"encoding/json"
	"errors"
	"sync/atomic"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"

	. "github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager/fake_stager"
)

var _ = Describe("Inbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var fauxstager *fake_stager.FakeStager
	var testingSink *steno.TestingSink
	var logger *steno.Logger
	var validator RequestValidator
	var stagingRequest models.StagingRequestFromCC

	BeforeEach(func() {
		testingSink = steno.NewTestingSink()
		stenoConfig := &steno.Config{
			Sinks: []steno.Sink{testingSink},
		}
		steno.Init(stenoConfig)

		stagingRequest = models.StagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}

		fakenats = fakeyagnats.New()
		fauxstager = &fake_stager.FakeStager{}
		logger = steno.NewLogger("fakelogger")
		validator = func(request models.StagingRequestFromCC) error {
			return nil
		}
	})

	listen := func() {
		Listen(fakenats, fauxstager, validator, logger)
	}

	publishStagingMessage := func() {
		msg, _ := json.Marshal(stagingRequest)
		fakenats.Publish(DiegoStageStartSubject, msg)
	}

	Context("when subscribing fails", func() {
		var attempts uint32

		BeforeEach(func() {
			fakenats.WhenSubscribing(DiegoStageStartSubject, func() error {
				atomic.AddUint32(&attempts, 1)
				return errors.New("oh no!")
			})

			go listen()
		})

		It("continues retrying until it succeeds", func() {
			Eventually(func() uint32 {
				return atomic.LoadUint32(&attempts)
			}).Should(BeNumerically(">=", 1))

			Eventually(func() uint32 {
				return atomic.LoadUint32(&attempts)
			}).Should(BeNumerically(">=", 2))

			Consistently(func() []yagnats.Subscription {
				return fakenats.Subscriptions(DiegoStageStartSubject)
			}).Should(BeEmpty())

			fakenats.WhenSubscribing(DiegoStageStartSubject, func() error {
				return nil
			})

			Eventually(func() []yagnats.Subscription {
				return fakenats.Subscriptions(DiegoStageStartSubject)
			}).ShouldNot(BeEmpty())

			publishStagingMessage()

			Ω(fauxstager.TimesStageInvoked).Should(Equal(1))
			Ω(fauxstager.StagingRequests[0]).Should(Equal(stagingRequest))
		})
	})

	Context("when it receives a staging request", func() {
		publishedCompletionMessages := func() []yagnats.Message {
			return fakenats.PublishedMessages("diego.staging.finished")
		}

		It("kicks off staging", func() {
			listen()

			publishStagingMessage()

			Ω(fauxstager.TimesStageInvoked).To(Equal(1))
			Ω(fauxstager.StagingRequests[0]).To(Equal(stagingRequest))
		})

		Context("when staging finishes successfully", func() {
			BeforeEach(listen)

			It("does not send a nats message", func() {
				publishStagingMessage()
				Ω(fakenats.PublishedMessages(outbox.DiegoStageFinishedSubject)).Should(HaveLen(0))
			})
		})

		Context("when staging finishes unsuccessfully", func() {
			BeforeEach(func() {
				fauxstager.AlwaysFail = true

				listen()
			})

			It("logs the failure", func() {
				Ω(testingSink.Records()).Should(HaveLen(0))

				publishStagingMessage()

				Ω(testingSink.Records()).ShouldNot(HaveLen(0))
			})

			It("sends a staging failure response", func() {
				publishStagingMessage()

				Ω(publishedCompletionMessages()).Should(HaveLen(1))
				response := publishedCompletionMessages()[0]

				stagingResponse := models.StagingResponseForCC{}
				json.Unmarshal(response.Payload, &stagingResponse)
				Ω(stagingResponse.Error).Should(Equal("Staging failed: The thingy broke :("))
			})
		})

		Context("when the request is invalid", func() {
			BeforeEach(func() {
				validator = func(models.StagingRequestFromCC) error {
					return errors.New("NO.")
				}

				listen()
			})

			It("logs the failure", func() {
				publishStagingMessage()
				Ω(testingSink.Records()).ShouldNot(HaveLen(0))
			})

			It("sends a staging failure response", func() {
				publishStagingMessage()

				Ω(publishedCompletionMessages()).Should(HaveLen(1))
				response := publishedCompletionMessages()[0]

				stagingResponse := models.StagingResponseForCC{}
				json.Unmarshal(response.Payload, &stagingResponse)

				Ω(stagingResponse).Should(Equal(models.StagingResponseForCC{
					AppId:  "myapp",
					TaskId: "mytask",
					Error:  "Invalid staging request: NO.",
				}))
			})
		})

		Context("when unmarshaling fails", func() {
			BeforeEach(listen)

			It("logs the failure", func() {
				Ω(testingSink.Records()).Should(BeEmpty())

				fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))

				Ω(testingSink.Records()).ShouldNot(BeEmpty())
			})

			It("does not send a message in response", func() {
				fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))
				Ω(publishedCompletionMessages()).Should(BeEmpty())
			})
		})
	})
})
