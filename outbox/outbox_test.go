package outbox_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/api_client"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
	"github.com/cloudfoundry/dropsonde/autowire/metrics"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/gunk/timeprovider/faketimeprovider"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("Outbox", func() {
	var (
		fakeCC               *ghttp.Server
		expectedBody         string
		ccResponseStatusCode int
		ccResponseBody       string

		logger lager.Logger
		task   models.Task
		bbs    *fake_bbs.FakeStagerBBS
		appId  string
		taskId string

		completedTasks chan models.Task
		watchStopChan  chan bool
		watchErrChan   chan error

		runner  *outbox.Outbox
		process ifrit.Process

		apiClient api_client.ApiClient

		fakeTimeProvider    *faketimeprovider.FakeTimeProvider
		metricSender        *fake.FakeMetricSender
		stagingDurationNano time.Duration
	)

	BeforeEach(func() {
		fakeCC = ghttp.NewServer()

		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))
		logger.Info("hello, world")
		appId = "my_app_id"
		taskId = "do_this"
		annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
			AppId:  appId,
			TaskId: taskId,
		})

		task = models.Task{
			Guid: "some-task-id",
			Result: `{
				"buildpack_key":"buildpack-key",
				"detected_buildpack":"Some Buildpack",
				"execution_metadata":"{\"start_command\":\"./some-start-command\"}",
				"detected_start_command":{"web":"./some-start-command"}
			}`,
			Annotation: string(annotationJson),
			Domain:     stager.TaskDomain,
		}

		completedTasks = make(chan models.Task, 1)
		watchStopChan = make(chan bool)
		watchErrChan = make(chan error, 1)
		bbs = &fake_bbs.FakeStagerBBS{}
		bbs.WatchForCompletedTaskReturns(completedTasks, watchStopChan, watchErrChan)

		ccResponseBody = `{}`

		fakeCC.AppendHandlers(
			handleStagingRequest(&ccResponseStatusCode, &ccResponseBody),
		)

		stagingDurationNano = 900900
		metricSender = fake.NewFakeMetricSender()
		metrics.Initialize(metricSender)

		apiClient = api_client.NewApiClient(fakeCC.URL(), "username", "password", true)

		fakeTimeProvider = faketimeprovider.New(time.Now())
		task.CreatedAt = fakeTimeProvider.Time().UnixNano()
		fakeTimeProvider.Increment(stagingDurationNano)

		runner = outbox.New(bbs, apiClient, logger, fakeTimeProvider)
	})

	JustBeforeEach(func() {
		process = ifrit.Invoke(runner)
	})

	AfterEach(func() {
		process.Signal(syscall.SIGTERM)
		Eventually(process.Wait()).Should(Receive())

		if fakeCC.HTTPTestServer != nil {
			fakeCC.Close()
		}
	})

	Context("when a completed staging task appears in the outbox", func() {
		BeforeEach(func() {
			completedTasks <- task
		})

		Context("when everything suceeds", func() {
			BeforeEach(func() {
				expectedBody = fmt.Sprintf(`{
					"buildpack_key":"buildpack-key",
					"detected_buildpack":"Some Buildpack",
					"execution_metadata":"{\"start_command\":\"./some-start-command\"}",
					"detected_start_command":{"web":"./some-start-command"},
					"app_id": "%s",
					"task_id": "%s"
			  }`, appId, taskId)

				ccResponseStatusCode = 200
			})

			It("resolves the completed task, then marks the Task as resolved", func() {
				Eventually(bbs.ResolvingTaskCallCount).Should(Equal(1))
				Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
				Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
			})

			It("posts the staging result to CC", func() {
				Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))
			})

			It("increments the staging success counter", func() {
				Eventually(func() uint64 {
					return metricSender.GetCounter("StagingRequestsSucceeded")
				}).Should(Equal(uint64(1)))
			})

			It("emits the time it took to stage succesfully", func() {
				Eventually(func() fake.Metric {
					return metricSender.GetValue("StagingRequestSucceededDuration")
				}).Should(Equal(fake.Metric{
					Value: float64(stagingDurationNano),
					Unit:  "nanos",
				}))
			})
		})

		Context("when the CC responds with an error", func() {
			BeforeEach(func() {
				ccResponseStatusCode = 500
			})

			It("does not attempt to resolve the Task", func() {
				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})

			PIt("marks the task as failed to resolve", func() {
			})
		})

		Context("when the CC connection fails", func() {
			BeforeEach(func() {
				fakeCC.Close()
			})

			It("does not attempt to resolve the Task", func() {
				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})

			PIt("marks the task as failed to resolve", func() {
			})
		})

		Context("when resolving the task fails", func() {
			BeforeEach(func() {
				bbs.ResolvingTaskReturns(errors.New("oops"))
			})

			It("does not send a response to the requester, because another stager probably resolved it", func() {
				Consistently(fakeCC.ReceivedRequests()).Should(HaveLen(0))
				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})
		})
	})

	Context("when the task is not a staging task", func() {
		BeforeEach(func() {
			task.Domain = "some-random-domain"
			completedTasks <- task
		})

		It("should not resolve the completed task ", func() {
			Consistently(bbs.ResolvingTaskCallCount).Should(BeZero())
		})

		It("should not post a response to the CC", func() {
			Consistently(fakeCC.ReceivedRequests()).Should(HaveLen(0))
		})
	})

	Context("when a completed docker staging task appears in the outbox", func() {
		BeforeEach(func() {
			task.Domain = stager_docker.TaskDomain
			task.Result = `{
				"execution_metadata":"{\"cmd\":\"./some-start-command\"}",
				"detected_start_command":{"web":"./some-start-command"}
			}`
			completedTasks <- task
		})

		Context("when everything suceeds", func() {
			BeforeEach(func() {
				expectedBody = fmt.Sprintf(`{
					"execution_metadata":"{\"cmd\":\"./some-start-command\"}",
					"detected_start_command":{"web":"./some-start-command"},
					"app_id": "%s",
					"task_id": "%s"
			  }`, appId, taskId)

				ccResponseStatusCode = 200
			})

			It("resolves the completed task, publishes its result and then marks the Task as resolved", func() {
				Eventually(bbs.ResolvingTaskCallCount).Should(Equal(1))
				Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))

				Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
				Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
			})
		})

		Context("when the CC responds with an error", func() {
			BeforeEach(func() {
				ccResponseStatusCode = 500
			})

			It("does not attempt to resolve the Task", func() {
				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})

			PIt("marks the task as failed to resolve", func() {
			})
		})

		Context("when the CC connection fails", func() {
			BeforeEach(func() {
				fakeCC.Close()
			})

			It("does not attempt to resolve the Task", func() {
				Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
			})

			PIt("marks the task as failed to resolve", func() {
			})
		})
	})

	Context("when an error is seen while watching", func() {
		BeforeEach(func() {
			watchErrChan <- errors.New("oh no!")
		})

		It("starts watching again", func() {
			sinceStart := time.Now()
			Eventually(bbs.WatchForCompletedTaskCallCount, 4).Should(Equal(2))
			Ω(time.Since(sinceStart)).Should(BeNumerically("~", 3*time.Second, 200*time.Millisecond))

			completedTasks <- task

			Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))
		})
	})

	Context("when a failed task appears in the outbox", func() {
		BeforeEach(func() {
			ccResponseStatusCode = 200
			expectedBody = fmt.Sprintf(`{
				"app_id":"%s",
				"buildpack_key": "",
				"detected_buildpack": "",
				"execution_metadata": "",
				"detected_start_command":null,
				"error":"because i said so",
				"task_id":"%s"
			}`, appId, taskId)

			task.Failed = true
			task.FailureReason = "because i said so"
			completedTasks <- task
		})

		It("publishes its reason as an error and then marks the task as completed", func() {
			Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))

			Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
			Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.Guid))
		})

		It("increments the staging success counter", func() {
			Eventually(fakeCC.ReceivedRequests).Should(HaveLen(1))

			Ω(metricSender.GetCounter("StagingRequestsFailed")).Should(Equal(uint64(1)))
		})

		It("emits the time it took to stage unsuccesfully", func() {
			Eventually(func() fake.Metric {
				return metricSender.GetValue("StagingRequestFailedDuration")
			}).Should(Equal(fake.Metric{
				Value: 900900,
				Unit:  "nanos",
			}))
		})
	})

	Describe("asynchronous message processing", func() {
		BeforeEach(func() {
			fakeCC.AppendHandlers(
				handleStagingRequest(&ccResponseStatusCode, &ccResponseBody),
				handleStagingRequest(&ccResponseStatusCode, &ccResponseBody),
			)
		})

		It("can accept new Completed Tasks before it's done processing existing tasks in the queue", func() {
			Eventually(completedTasks).Should(BeSent(task))
			Eventually(completedTasks).Should(BeSent(task))
			Eventually(completedTasks).Should(BeSent(task))

			Eventually(fakeCC.ReceivedRequests).Should(HaveLen(3))
		})
	})
})

func base64Encode(input string) string {
	return base64.StdEncoding.EncodeToString([]byte(input))
}

func handleStagingRequest(ccResponseStatusCode *int, ccResponseBody *string) http.HandlerFunc {
	return ghttp.CombineHandlers(
		ghttp.VerifyRequest("POST", "/internal/staging/completed"),
		ghttp.VerifyBasicAuth("username", "password"),
		ghttp.RespondWithPtr(ccResponseStatusCode, ccResponseBody),
	)
}
