package outbox_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/outbox"
	steno "github.com/cloudfoundry/gosteno"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("Outbox", func() {
	var (
		fakenats  *fakeyagnats.FakeYagnats
		logger    *steno.Logger
		task      models.Task
		bbs       *fake_bbs.FakeStagerBBS
		published <-chan []byte
		appId     string
		taskId    string

		outbox ifrit.Process
	)

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = steno.NewLogger("fakelogger")
		appId = "my_app_id"
		taskId = "do_this"
		annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
			AppId:  appId,
			TaskId: taskId,
		})

		task = models.Task{
			Guid:       "some-task-id",
			Result:     "{}",
			Annotation: string(annotationJson),
			Type:       models.TaskTypeStaging,
		}
		bbs = fake_bbs.NewFakeStagerBBS()

		publishedCallback := make(chan []byte)

		published = publishedCallback

		fakenats.Subscribe(DiegoStageFinishedSubject, func(msg *yagnats.Message) {
			publishedCallback <- msg.Payload
		})
	})

	JustBeforeEach(func(done Done) {
		go func() {
			outbox = ifrit.Envoke(New(bbs, fakenats, logger))
		}()
		Eventually(bbs.WatchingForCompleted()).Should(Receive())
		close(done)
	})

	AfterEach(func(done Done) {
		outbox.Signal(syscall.SIGTERM)
		<-outbox.Wait()
		close(done)
	})

	Context("when a completed Task appears in the outbox", func() {
		BeforeEach(func() {
			task.Result = `{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack",
				"detected_start_command":"./some-start-command"
			}`
		})

		It("resolves the completed task, publishes its result and then marks the Task as resolved", func() {
			bbs.SendCompletedTask(task)

			Eventually(bbs.ResolvingTaskInput).ShouldNot(BeZero())

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack",
				"detected_start_command":"./some-start-command",
				"app_id": "%s",
				"task_id": "%s"
			}`, appId, taskId)))

			Eventually(bbs.ResolvedTask).Should(Equal(task))
		})

		Context("when the task is not a staging task", func() {
			It("Should not resolve the completed task ", func() {
				task.Type = models.TaskTypeDropletMigration
				bbs.SendCompletedTask(task)
				Consistently(bbs.ResolvingTaskInput).Should(BeZero())
			})
		})

		Context("when the response fails to go out", func() {
			BeforeEach(func() {
				fakenats.WhenPublishing(DiegoStageFinishedSubject, func(msg *yagnats.Message) error {
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
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"error":"because i said so",
				"app_id":"%s",
				"task_id":"%s"
			}`, appId, taskId)))

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
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"detected_buildpack":"Some Buildpack",
				"app_id": "%s",
				"task_id": "%s"
			}`, appId, taskId)))

			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"detected_buildpack":"Some Buildpack",
				"app_id": "%s",
				"task_id": "%s"
			}`, appId, taskId)))
		})
	})
})
