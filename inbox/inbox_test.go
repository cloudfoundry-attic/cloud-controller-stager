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

	"github.com/apcera/nats"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/api_client/fakes"
	. "github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/stager/fake_stager"
	"github.com/cloudfoundry/gunk/diegonats"
)

var _ = Describe("Inbox", func() {
	var fakenats *diegonats.FakeNATSClient
	var fakeapi *fakes.FakeApiClient
	var fauxstager *fake_stager.FakeStager
	var logOutput *gbytes.Buffer
	var logger lager.Logger
	var validator RequestValidator
	var stagingRequest cc_messages.StagingRequestFromCC

	var inbox ifrit.Process

	BeforeEach(func() {
		logOutput = gbytes.NewBuffer()
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(logOutput, lager.INFO))

		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}

		fakenats = diegonats.NewFakeClient()
		fakeapi = &fakes.FakeApiClient{}
		fauxstager = &fake_stager.FakeStager{}
		validator = func(request cc_messages.StagingRequestFromCC) error {
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
			fakenats.WhenSubscribing(DiegoStageStartSubject, func(callback nats.MsgHandler) error {
				atomic.AddUint32(&attempts, 1)
				return errors.New("oh no!")
			})
		})

		JustBeforeEach(func() {
			process = make(chan ifrit.Process)
			go func() {
				process <- ifrit.Invoke(New(fakenats, fakeapi, fauxstager, nil, validator, logger))
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

			Consistently(func() []*nats.Subscription {
				return fakenats.Subscriptions(DiegoStageStartSubject)
			}).Should(BeEmpty())

			fakenats.WhenSubscribing(DiegoStageStartSubject, func(callback nats.MsgHandler) error {
				return nil
			})

			Eventually(func() []*nats.Subscription {
				return fakenats.Subscriptions(DiegoStageStartSubject)
			}).ShouldNot(BeEmpty())
		})
	})

	Context("when subscribing succeeds", func() {
		JustBeforeEach(func() {
			inbox = ifrit.Envoke(New(fakenats, fakeapi, fauxstager, nil, validator, logger))
		})

		AfterEach(func(done Done) {
			inbox.Signal(syscall.SIGTERM)
			<-inbox.Wait()
			close(done)
		})

		Context("and it receives a staging request", func() {
			It("kicks off staging", func() {
				publishStagingMessage()

				Ω(fauxstager.TimesStageInvoked).To(Equal(1))
				Ω(fauxstager.StagingRequests[0]).To(Equal(stagingRequest))
			})

			Context("when staging finishes successfully", func() {
				It("does not send a staging complete message", func() {
					publishStagingMessage()
					Ω(fakeapi.StagingCompleteCallCount()).To(Equal(0))
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

					Ω(fakeapi.StagingCompleteCallCount()).To(Equal(1))
					response, _ := fakeapi.StagingCompleteArgsForCall(0)

					stagingResponse := cc_messages.StagingResponseForCC{}
					json.Unmarshal(response, &stagingResponse)
					Ω(stagingResponse.Error).Should(Equal("Staging failed: The thingy broke :("))
				})
			})

			Context("when the request is invalid", func() {
				BeforeEach(func() {
					validator = func(cc_messages.StagingRequestFromCC) error {
						return errors.New("NO.")
					}
				})

				It("logs the failure", func() {
					publishStagingMessage()
					Ω(logOutput.Contents()).ShouldNot(BeEmpty())
				})

				It("sends a staging failure response", func() {
					publishStagingMessage()

					Ω(fakeapi.StagingCompleteCallCount()).To(Equal(1))
					response, _ := fakeapi.StagingCompleteArgsForCall(0)

					stagingResponse := cc_messages.StagingResponseForCC{}
					json.Unmarshal(response, &stagingResponse)

					Ω(stagingResponse).Should(Equal(cc_messages.StagingResponseForCC{
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

					Ω(logOutput).Should(gbytes.Say("malformed"))
				})

				It("does not send a message in response", func() {
					fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))
					Ω(fakeapi.StagingCompleteCallCount()).To(Equal(0))
				})
			})
		})
	})
})
