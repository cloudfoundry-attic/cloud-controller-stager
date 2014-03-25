package outbox_test

import (
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/outbox"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Outbox", func() {
	var fakenats *fakeyagnats.FakeYagnats
	var logger *steno.Logger
	var runOnce *models.RunOnce
	var bbs *fake_bbs.FakeStagerBBS
	var published chan []byte

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")
		runOnce = &models.RunOnce{
			Guid:    "some-task-id",
			ReplyTo: "some-requester",
			Result:  "{}",
		}
		bbs = fake_bbs.NewFakeStagerBBS()
		published = make(chan []byte)

		fakenats.Subscribe("some-requester", func(msg *yagnats.Message) {
			published <- msg.Payload
		})
	})

	JustBeforeEach(func() {
		go Listen(bbs, fakenats, logger)

		Eventually(bbs.WatchingForCompleted()).Should(Receive())
	})

	Context("when a completed RunOnce appears in the outbox", func() {
		BeforeEach(func() {
			runOnce.Result = `{"detected_buildpack":"Some Buildpack"}`
		})

		It("claims the completed runonce, publishes its result to ReplyTo and then marks the RunOnce as completed", func() {
			bbs.SendCompletedRunOnce(runOnce)

			Eventually(bbs.ResolvingRunOnceInput).ShouldNot(BeZero())

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"detected_buildpack":"Some Buildpack"}`))

			Eventually(bbs.ResolvedRunOnce).Should(Equal(runOnce))
		})

		Context("when the response fails to go out", func() {
			BeforeEach(func() {
				fakenats.WhenPublishing("some-requester", func() error {
					return errors.New("kaboom!")
				})
			})

			It("does not attempt to resolve the RunOnce", func() {
				bbs.SendCompletedRunOnce(runOnce)

				Consistently(bbs.ResolvedRunOnce).ShouldNot(Equal(runOnce))
			})
		})
	})

	Context("when a failed RunOnce appears in the outbox", func() {
		BeforeEach(func() {
			runOnce.Failed = true
			runOnce.FailureReason = "because i said so"
		})

		It("publishes its reason as an error to ReplyTo and then marks the RunOnce as completed", func() {
			bbs.SendCompletedRunOnce(runOnce)

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"error":"because i said so"}`))

			Eventually(bbs.ResolvedRunOnce).Should(Equal(runOnce))
		})
	})

	Context("when ResolvingRunOnce fails", func() {
		BeforeEach(func() {
			bbs.WhenSettingResolving(func() error {
				return errors.New("oops")
			})
		})

		It("does not send a response to the requester, because another stager probably resolved it", func() {
			bbs.SendCompletedRunOnce(runOnce)

			Consistently(bbs.ResolvedRunOnce).ShouldNot(Equal(runOnce))
			Consistently(published).ShouldNot(Receive())
		})
	})

	Context("when an error is seen while watching", func() {
		It("starts watching again", func() {
			bbs.SendCompletedRunOnceWatchError(errors.New("oh no!"))

			Eventually(bbs.WatchingForCompleted()).Should(Receive())

			bbs.SendCompletedRunOnce(runOnce)
			Eventually(published).Should(Receive())

			Eventually(bbs.ResolvedRunOnce).Should(Equal(runOnce))
		})
	})

	Describe("asynchronous message processing", func() {
		It("can accept new Completed RunOnces before it's done processing existing RunOnces in the queue", func() {
			runOnce.Result = `{"detected_buildpack":"Some Buildpack"}`

			bbs.SendCompletedRunOnce(runOnce)
			bbs.SendCompletedRunOnce(runOnce)

			var receivedPayload []byte

			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"detected_buildpack":"Some Buildpack"}`))

			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"detected_buildpack":"Some Buildpack"}`))
		})
	})
})
