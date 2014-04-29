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
	var task *models.Task
	var bbs *fake_bbs.FakeStagerBBS
	var published chan []byte

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")
		task = &models.Task{
			Guid:   "some-task-id",
			Result: "{}",
		}
		bbs = fake_bbs.NewFakeStagerBBS()

		publishedCallback := make(chan []byte)

		published = publishedCallback

		fakenats.Subscribe(DiegoStageFinishedSubject, func(msg *yagnats.Message) {
			publishedCallback <- msg.Payload
		})
	})

	JustBeforeEach(func() {
		go Listen(bbs, fakenats, logger)

		Eventually(bbs.WatchingForCompleted()).Should(Receive())
	})

	Context("when a completed Task appears in the outbox", func() {
		BeforeEach(func() {
			task.Result = `{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack"
			}`
		})

		It("claims the completed task, publishes its result and then marks the Task as completed", func() {
			bbs.SendCompletedTask(task)

			Eventually(bbs.ResolvingTaskInput).ShouldNot(BeZero())

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack"
			}`))

			Eventually(bbs.ResolvedTask).Should(Equal(task))
		})

		Context("when the response fails to go out", func() {
			BeforeEach(func() {
				fakenats.WhenPublishing("some-requester", func() error {
					return errors.New("kaboom!")
				})
			})

			It("does not attempt to resolve the Task", func() {
				bbs.SendCompletedTask(task)

				Consistently(bbs.ResolvedTask).ShouldNot(Equal(task))
			})
		})
	})

	Context("when a failed Task appears in the outbox", func() {
		BeforeEach(func() {
			task.Failed = true
			task.FailureReason = "because i said so"
		})

		It("publishes its reason as an error and then marks the Task as completed", func() {
			bbs.SendCompletedTask(task)

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"error":"because i said so"}`))

			Eventually(bbs.ResolvedTask).Should(Equal(task))
		})
	})

	Context("when ResolvingTask fails", func() {
		BeforeEach(func() {
			bbs.WhenSettingResolving(func() error {
				return errors.New("oops")
			})
		})

		It("does not send a response to the requester, because another stager probably resolved it", func() {
			bbs.SendCompletedTask(task)

			Consistently(bbs.ResolvedTask).ShouldNot(Equal(task))
			Consistently(published).ShouldNot(Receive())
		})
	})

	Context("when an error is seen while watching", func() {
		It("starts watching again", func() {
			bbs.SendCompletedTaskWatchError(errors.New("oh no!"))

			Eventually(bbs.WatchingForCompleted()).Should(Receive())

			bbs.SendCompletedTask(task)
			Eventually(published).Should(Receive())

			Eventually(bbs.ResolvedTask).Should(Equal(task))
		})
	})

	Describe("asynchronous message processing", func() {
		It("can accept new Completed Tasks before it's done processing existing Tasks in the queue", func() {
			task.Result = `{"detected_buildpack":"Some Buildpack"}`

			bbs.SendCompletedTask(task)
			bbs.SendCompletedTask(task)

			var receivedPayload []byte

			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"detected_buildpack":"Some Buildpack"}`))

			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(`{"detected_buildpack":"Some Buildpack"}`))
		})
	})
})
