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
	"github.com/cloudfoundry-incubator/stager/stager/fake_stager"
)

var _ = Describe("Inbox", func() {
	Context("when it receives a staging request", func() {
		var fakenats *fakeyagnats.FakeYagnats
		var fauxstager *fake_stager.FakeStager
		var testingSink *steno.TestingSink
		var logger *steno.Logger
		var validator RequestValidator

		BeforeEach(func() {
			testingSink = steno.NewTestingSink()
			stenoConfig := &steno.Config{
				Sinks: []steno.Sink{testingSink},
			}
			steno.Init(stenoConfig)

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

		It("kicks off staging", func() {
			Listen(fakenats, fauxstager, validator, logger)

			stagingRequest := models.StagingRequestFromCC{
				AppId:  "myapp",
				TaskId: "mytask",
			}
			msg, _ := json.Marshal(stagingRequest)

			fakenats.Publish("diego.staging.start", msg)
			Ω(fauxstager.TimesStageInvoked).To(Equal(1))
			Ω(fauxstager.StagingRequests[0]).To(Equal(stagingRequest))
		})

		Context("when subscribing fails", func() {
			var attempts uint32

			BeforeEach(func() {
				fakenats.WhenSubscribing("diego.staging.start", func() error {
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
					return fakenats.Subscriptions("diego.staging.start")
				}).Should(BeEmpty())

				fakenats.WhenSubscribing("diego.staging.start", func() error {
					return nil
				})

				Eventually(func() []yagnats.Subscription {
					return fakenats.Subscriptions("diego.staging.start")
				}).ShouldNot(BeEmpty())

				stagingRequest := models.StagingRequestFromCC{
					AppId:  "myapp",
					TaskId: "mytask",
				}
				msg, _ := json.Marshal(stagingRequest)

				fakenats.Publish("diego.staging.start", msg)

				Ω(fauxstager.TimesStageInvoked).Should(Equal(1))
				Ω(fauxstager.StagingRequests[0]).Should(Equal(stagingRequest))
			})
		})

		Context("when unmarshaling fails", func() {
			BeforeEach(listen)

			It("logs the failure", func() {
				Ω(testingSink.Records()).Should(HaveLen(0))

				fakenats.PublishWithReplyTo("diego.staging.start", "reply string", []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))

				Ω(testingSink.Records()).ShouldNot(HaveLen(0))
			})

			It("sends a staging failure response", func() {
				fakenats.PublishWithReplyTo("diego.staging.start", "reply string", []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))
				replyTo := fakenats.PublishedMessages("diego.staging.start")[0].ReplyTo

				Ω(fakenats.PublishedMessages(replyTo)).Should(HaveLen(1))
				response := fakenats.PublishedMessages(replyTo)[0]
				stagingResponse := models.StagingResponseForCC{}
				json.Unmarshal(response.Payload, &stagingResponse)
				Ω(stagingResponse.Error).Should(Equal("Staging message contained malformed JSON"))
			})
		})

		publishStagingMessage := func() {
			stagingRequest := models.StagingRequestFromCC{
				AppId:  "myapp",
				TaskId: "mytask",
			}
			msg, _ := json.Marshal(stagingRequest)

			fakenats.PublishWithReplyTo("diego.staging.start", "reply to", msg)
		}

		Context("when staging finishes successfully", func() {
			BeforeEach(listen)

			It("does not send a nats message", func() {
				publishStagingMessage()

				replyTo := fakenats.PublishedMessages("diego.staging.start")[0].ReplyTo

				Ω(fakenats.PublishedMessages(replyTo)).Should(HaveLen(0))
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

				replyTo := fakenats.PublishedMessages("diego.staging.start")[0].ReplyTo

				Ω(fakenats.PublishedMessages(replyTo)).Should(HaveLen(1))
				response := fakenats.PublishedMessages(replyTo)[0]
				stagingResponse := models.StagingResponseForCC{}
				json.Unmarshal(response.Payload, &stagingResponse)
				Ω(stagingResponse.Error).Should(Equal("Invalid staging request: NO."))
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

				replyTo := fakenats.PublishedMessages("diego.staging.start")[0].ReplyTo

				Ω(fakenats.PublishedMessages(replyTo)).Should(HaveLen(1))
				response := fakenats.PublishedMessages(replyTo)[0]
				stagingResponse := models.StagingResponseForCC{}
				json.Unmarshal(response.Payload, &stagingResponse)
				Ω(stagingResponse.Error).Should(Equal("Staging failed: The thingy broke :("))
			})
		})
	})
})
