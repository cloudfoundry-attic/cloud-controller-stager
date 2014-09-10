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

	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"

	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	. "github.com/cloudfoundry-incubator/stager/inbox"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager/fake_stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker/fake_stager_docker"
)

var _ = Describe("Docker Inbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var fauxstager *fake_stager.FakeStager
	var fauxstagerdocker *fake_stager_docker.FakeStagerDocker
	var logOutput *gbytes.Buffer
	var logger lager.Logger
	var validator RequestValidator
	var stagingRequest cc_messages.DockerStagingRequestFromCC

	var inbox ifrit.Process

	BeforeEach(func() {
		logOutput = gbytes.NewBuffer()
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(logOutput, lager.INFO))

		stagingRequest = cc_messages.DockerStagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}

		fakenats = fakeyagnats.New()
		fauxstager = &fake_stager.FakeStager{}
		fauxstagerdocker = &fake_stager_docker.FakeStagerDocker{}
		validator = func(request cc_messages.StagingRequestFromCC) error {
			return nil
		}
	})

	publishStagingMessage := func() {
		msg, _ := json.Marshal(stagingRequest)
		fakenats.Publish(DiegoDockerStageStartSubject, msg)
	}

	Context("when subscribing fails", func() {
		var attempts uint32
		var process chan ifrit.Process

		BeforeEach(func() {
			fakenats.WhenSubscribing(DiegoDockerStageStartSubject, func(callback yagnats.Callback) error {
				atomic.AddUint32(&attempts, 1)
				return errors.New("oh no!")
			})
		})

		JustBeforeEach(func() {
			process = make(chan ifrit.Process)
			go func() {
				process <- ifrit.Envoke(New(fakenats, fauxstager, fauxstagerdocker, validator, logger))
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
				return fakenats.Subscriptions(DiegoDockerStageStartSubject)
			}).Should(BeEmpty())

			fakenats.WhenSubscribing(DiegoDockerStageStartSubject, func(callback yagnats.Callback) error {
				return nil
			})

			Eventually(func() []yagnats.Subscription {
				return fakenats.Subscriptions(DiegoDockerStageStartSubject)
			}).ShouldNot(BeEmpty())
		})
	})

	Context("when subscribing succeeds", func() {
		JustBeforeEach(func() {
			inbox = ifrit.Envoke(New(fakenats, fauxstager, fauxstagerdocker, validator, logger))
		})

		AfterEach(func(done Done) {
			inbox.Signal(syscall.SIGTERM)
			<-inbox.Wait()
			close(done)
		})

		Context("and it receives a staging request", func() {

			It("kicks off staging", func() {
				publishStagingMessage()

				Ω(fauxstagerdocker.TimesStageInvoked).To(Equal(1))
				Ω(fauxstagerdocker.StagingRequests[0]).To(Equal(stagingRequest))
			})
			Context("when staging finishes successfully", func() {
				It("does not send a nats message", func() {
					publishStagingMessage()
					Ω(fakenats.PublishedMessages(outbox.DiegoDockerStageFinishedSubject)).Should(HaveLen(0))
				})
			})
			Context("when unmarshaling fails", func() {
				It("logs the failure", func() {
					Ω(logOutput.Contents()).Should(BeEmpty())

					fakenats.Publish(DiegoDockerStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))

					Ω(logOutput).Should(gbytes.Say("malformed"))
				})

				It("does not send a message in response", func() {
					fakenats.Publish(DiegoStageStartSubject, []byte("fdsaljkfdsljkfedsews:/sdfa:''''"))
					stagingCompleteMessages := fakenats.PublishedMessages(outbox.DiegoDockerStageFinishedSubject)
					Ω(stagingCompleteMessages).Should(BeEmpty())
				})
			})
		})
	})
})
