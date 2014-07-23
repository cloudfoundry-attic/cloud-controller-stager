package outbox_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry/yagnats"
	"github.com/cloudfoundry/yagnats/fakeyagnats"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("Outbox", func() {
	var (
		fakenats  *fakeyagnats.FakeYagnats
		logger    lager.Logger
		task      models.Task
		bbs       *fake_bbs.FakeStagerBBS
		published <-chan []byte
		appId     string
		taskId    string

		completedTasks chan models.Task
		watchStopChan  chan bool
		watchErrChan   chan error

		outbox ifrit.Process
	)

	BeforeEach(func() {
		fakenats = fakeyagnats.New()
		logger = lager.NewLogger("fakelogger")
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

		completedTasks = make(chan models.Task)
		watchStopChan = make(chan bool)
		watchErrChan = make(chan error)
		bbs = &fake_bbs.FakeStagerBBS{}
		bbs.WatchForCompletedTaskReturns(completedTasks, watchStopChan, watchErrChan)

		publishedCallback := make(chan []byte)

		published = publishedCallback

		fakenats.Subscribe(DiegoStageFinishedSubject, func(msg *yagnats.Message) {
			publishedCallback <- msg.Payload
		})
	})

	JustBeforeEach(func() {
		outbox = ifrit.Envoke(New(bbs, fakenats, logger))
	})

	AfterEach(func() {
		outbox.Signal(syscall.SIGTERM)
		Eventually(outbox.Wait()).Should(Receive())
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
			completedTasks <- task

			Eventually(bbs.ResolvingTaskCallCount).Should(Equal(1))

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack",
				"detected_start_command":"./some-start-command",
				"app_id": "%s",
				"task_id": "%s"
			}`, appId, taskId)))

			Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
			Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
		})

		Context("when the task is not a staging task", func() {
			It("Should not resolve the completed task ", func() {
				task.Type = models.TaskTypeDropletMigration
				completedTasks <- task
				Consistently(bbs.ResolvingTaskCallCount).Should(BeZero())
			})
		})

		Context("when the response fails to go out", func() {
			BeforeEach(func() {
				fakenats.WhenPublishing(DiegoStageFinishedSubject, func(msg *yagnats.Message) error {
					return errors.New("kaboom!")
				})
			})

			It("does not attempt to resolve the Task", func() {
				completedTasks <- task

				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})
		})
	})

	Context("when a failed Task appears in the outbox", func() {
		BeforeEach(func() {
			task.Failed = true
			task.FailureReason = "because i said so"
		})

		It("publishes its reason as an error and then marks the Task as completed", func() {
			completedTasks <- task

			var receivedPayload []byte
			Eventually(published).Should(Receive(&receivedPayload))
			Ω(receivedPayload).Should(MatchJSON(fmt.Sprintf(`{
				"error":"because i said so",
				"app_id":"%s",
				"task_id":"%s"
			}`, appId, taskId)))

			Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
			Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
		})
	})

	Context("when ResolvingTask fails", func() {
		BeforeEach(func() {
			bbs.ResolvingTaskReturns(errors.New("oops"))
		})

		It("does not send a response to the requester, because another stager probably resolved it", func() {
			completedTasks <- task

			Consistently(published).ShouldNot(Receive())
			Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
		})
	})

	Context("when an error is seen while watching", func() {
		It("starts watching again", func() {
			sinceStart := time.Now()

			watchErrChan <- errors.New("oh no!")

			Eventually(bbs.WatchForCompletedTaskCallCount, 4).Should(Equal(2))

			Ω(time.Since(sinceStart)).Should(BeNumerically("~", 3*time.Second, 200*time.Millisecond))

			completedTasks <- task
			Eventually(published).Should(Receive())
			Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
			Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
		})
	})

	Describe("asynchronous message processing", func() {
		It("can accept new Completed Tasks before it's done processing existing Tasks in the queue", func() {
			task.Result = `{"detected_buildpack":"Some Buildpack"}`

			completedTasks <- task
			completedTasks <- task

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
