package inbox_test

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"syscall"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"

	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"

	. "github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager/fake_stager"
)

var _ = Describe("Inbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var fauxstager *fake_stager.FakeStager
	var logOutput *gbytes.Buffer
	var logger lager.Logger
	var validator RequestValidator
	var stagingRequest models.StagingRequestFromCC

	var inbox ifrit.Process

	BeforeEach(func() {
		logOutput = gbytes.NewBuffer()
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(logOutput, lager.INFO))

		stagingRequest = models.StagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}

		fakenats = fakeyagnats.New()
		fauxstager = &fake_stager.FakeStager{}
		validator = func(request models.StagingRequestFromCC) error {
			return nil
		}
	})

	publishStagingMessage := func() {
		msg, _ := json.Marshal(stagingRequest)
		fakenats.Publish(DiegoStageStartSubject, msg)
	}

	Context("when subscribing fails", func() {
		var attempts uint32
		var process chan ifrit.Process

		BeforeEach(func() {
			fakenats.WhenSubscribing(DiegoStageStartSubject, func(callback yagnats.Callback) error {
				atomic.AddUint32(&attempts, 1)
				return errors.New("oh no!")
			})
		})

		JustBeforeEach(func() {
			process = make(chan ifrit.Process)
			go func() {
				process <- ifrit.Envoke(New(fakenats, fauxstager, validator, logger))
			}()
		})

		AfterEach(func(done Done) {
			p := <-process
			p.Signal(syscall.SIGTERM)
			<-p.Wait()
			close(done)
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

			fakenats.WhenSubscribing(DiegoStageStartSubject, func(callback yagnats.Callback) error {
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

	Context("when subscribing succeeds", func() {
		JustBeforeEach(func() {
			inbox = ifrit.Envoke(New(fakenats, fauxstager, validator, logger))
		})

		AfterEach(func(done Done) {
			inbox.Signal(syscall.SIGTERM)
			<-inbox.Wait()
			close(done)
		})

		Context("and it receives a staging request", func() {
			publishedCompletionMessages := func() []yagnats.Message {
				return fakenats.PublishedMessages("diego.staging.finished")
			}

			It("kicks off staging", func() {
				publishStagingMessage()

				Ω(fauxstager.TimesStageInvoked).To(Equal(1))
				Ω(fauxstager.StagingRequests[0]).To(Equal(stagingRequest))
			})

			Context("when staging finishes successfully", func() {
				It("does not send a nats message", func() {
					publishStagingMessage()
					Ω(fakenats.PublishedMessages(outbox.DiegoStageFinishedSubject)).Should(HaveLen(0))
				})
			})

			Context("when staging finishes unsuccessfully", func() {
				BeforeEach(func() {
					fauxstager.AlwaysFail = true
				})

				It("logs the failure", func() {
					Ω(logOutput.Contents()).Should(BeEmpty())

					publishStagingMessage()

					Ω(logOutput).Should(gbytes.Say("failed"))
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
				})

				It("logs the failure", func() {
					publishStagingMessage()
					Ω(logOutput.Contents()).ShouldNot(BeEmpty())
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
				It("logs the failure", func() {
					Ω(logOutput.Contents()).Should(BeEmpty())

					fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))

					Ω(logOutput.Contents()).ShouldNot(BeEmpty())
				})

				It("does not send a message in response", func() {
					fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))
					Ω(publishedCompletionMessages()).Should(BeEmpty())
				})
			})
		})
	})
})
