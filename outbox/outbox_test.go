package outbox_test

import (
	"errors"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fakebbs"
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
	var runOnce models.RunOnce
	var bbs *fakebbs.FakeStagerBBS
	var published chan []byte

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")
		runOnce = models.RunOnce{
			Guid:    "some-task-id",
			ReplyTo: "some-requester",
			Result:  "{}",
		}
		bbs = fakebbs.NewFakeStagerBBS()
		published = make(chan []byte)

		fakenats.Subscribe("some-requester", func(msg *yagnats.Message) {
			published <- msg.Payload
		})
	})

	JustBeforeEach(func() {
		go Listen(bbs, fakenats, logger)
		<-bbs.CalledCompletedRunOnce
	})

	Context("when a completed RunOnce appears in the outbox", func() {
		BeforeEach(func() {
			runOnce.Result = `{"detected_buildpack":"Some Buildpack"}`
		})

		It("claims the completed runonce, publishes its result to ReplyTo and then marks the RunOnce as completed", func(done Done) {
			bbs.CompletedRunOnceChan <- runOnce

			Ω(string(<-published)).Should(Equal(`{"detected_buildpack":"Some Buildpack"}`))
			Ω(bbs.ResolvingRunOnceInput.RunOnceToResolve).ShouldNot(BeZero())
			Ω(bbs.ResolvedRunOnce).Should(Equal(runOnce))

			close(done)
		}, 5.0)

		Context("when the response fails to go out", func() {
			It("does not attempt to resolve the RunOnce", func(done Done) {
				fakenats.PublishError = errors.New("kaboom!")

				bbs.CompletedRunOnceChan <- runOnce
				Consistently(bbs.ResolvedRunOnce).ShouldNot(Equal(runOnce))
				close(done)
			}, 5.0)
		})
	})

	Context("when a failed RunOnce appears in the outbox", func() {
		BeforeEach(func() {
			runOnce.Failed = true
			runOnce.FailureReason = "because i said so"
		})

		It("publishes its reason as an error to ReplyTo and then marks the RunOnce as completed", func(done Done) {
			bbs.CompletedRunOnceChan <- runOnce

			Ω(string(<-published)).Should(Equal(`{"error":"because i said so"}`))

			Ω(bbs.ResolvedRunOnce).Should(Equal(runOnce))

			close(done)
		}, 5.0)
	})

	Context("when ResolvingRunOnce fails", func() {
		BeforeEach(func() {
			bbs.ResolvingRunOnceOutput.Err = errors.New("oops")
		})

		It("does not send a response to the requester, because another stager probably resolved it", func(done Done) {
			bbs.CompletedRunOnceChan <- runOnce

			Consistently(bbs.ResolvedRunOnce).ShouldNot(Equal(runOnce))
			Consistently(published).ShouldNot(Receive())
			close(done)
		}, 5.0)
	})

	Context("when an error is seen while watching", func() {

		It("starts watching again", func(done Done) {
			bbs.CompletedRunOnceErrChan <- errors.New("hell")

			<-bbs.CalledCompletedRunOnce

			bbs.CompletedRunOnceChan <- runOnce
			<-published

			Ω(bbs.ResolvedRunOnce).Should(Equal(runOnce))
			close(done)
		}, 2.0)
	})

	Describe("asynchronous message processing", func() {
		It("can accept new Completed RunOnces before it's done processing existing RunOnces in the queue", func(done Done) {
			runOnce.Result = `{"detected_buildpack":"Some Buildpack"}`
			bbs.CompletedRunOnceChan <- runOnce
			bbs.CompletedRunOnceChan <- runOnce

			Ω(string(<-published)).Should(Equal(`{"detected_buildpack":"Some Buildpack"}`))
			Ω(string(<-published)).Should(Equal(`{"detected_buildpack":"Some Buildpack"}`))
			close(done)
		})
	})
})
