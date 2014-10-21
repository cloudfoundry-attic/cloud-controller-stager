package outbox_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/api_client"
	"github.com/cloudfoundry-incubator/stager/api_client/fakes"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry-incubator/stager/stager_docker"
	"github.com/cloudfoundry/dropsonde/autowire/metrics"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/gunk/timeprovider/faketimeprovider"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("Outbox", func() {
	var (
		expectedBody []byte

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

		fakeApiClient       *fakes.FakeApiClient
		fakeTimeProvider    *faketimeprovider.FakeTimeProvider
		metricSender        *fake.FakeMetricSender
		stagingDurationNano time.Duration
	)

	BeforeEach(func() {
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		appId = "my_app_id"
		taskId = "do_this"
		annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
			AppId:  appId,
			TaskId: taskId,
		})

		task = models.Task{
			TaskGuid: "some-task-id",
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

		stagingDurationNano = 900900
		metricSender = fake.NewFakeMetricSender()
		metrics.Initialize(metricSender)

		fakeApiClient = &fakes.FakeApiClient{}

		fakeTimeProvider = faketimeprovider.New(time.Now())
		task.CreatedAt = fakeTimeProvider.Time().UnixNano()
		fakeTimeProvider.Increment(stagingDurationNano)

		runner = outbox.New(bbs, fakeApiClient, logger, fakeTimeProvider)
	})

	JustBeforeEach(func() {
		process = ifrit.Invoke(runner)
	})

	AfterEach(func() {
		process.Signal(syscall.SIGTERM)
		Eventually(process.Wait()).Should(Receive())
	})

	Context("when a completed staging task appears in the outbox", func() {
		JustBeforeEach(func() {
			completedTasks <- task
		})

		Context("when everything suceeds", func() {
			BeforeEach(func() {
				expectedBody = []byte(fmt.Sprintf(`{
					"buildpack_key":"buildpack-key",
					"detected_buildpack":"Some Buildpack",
					"execution_metadata":"{\"start_command\":\"./some-start-command\"}",
					"detected_start_command":{"web":"./some-start-command"},
					"app_id": "%s",
					"task_id": "%s"
				}`, appId, taskId))

			})

			It("resolves the completed task, then marks the Task as resolved", func() {
				Eventually(bbs.ResolvingTaskCallCount).Should(Equal(1))
				Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
				Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.TaskGuid))
			})

			It("posts the staging result to CC", func() {
				Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(1))
				payload, _ := fakeApiClient.StagingCompleteArgsForCall(0)
				Ω(payload).Should(MatchJSON(expectedBody))
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

		Context("when POSTing the staging-complete message fails", func() {
			BeforeEach(func() {
				fakeApiClient.StagingCompleteReturns(&api_client.BadResponseError{504})
			})

			It("increments the staging failed to resolve counter", func() {
				Eventually(func() uint64 {
					return metricSender.GetCounter("StagingFailedToResolve")
				}).Should(Equal(uint64(1)))
			})

			Context("when the api client error is retryable", func() {
				BeforeEach(func() {
					fakeApiClient.StagingCompleteReturns(&api_client.BadResponseError{503})
				})

				It("retries delivering the StagingComplete message for a limited number of times", func() {
					Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(outbox.StagingResponseRetryLimit))
					Consistently(fakeApiClient.StagingCompleteCallCount).Should(Equal(outbox.StagingResponseRetryLimit))
				})

				It("only increments the staging failed to resolve counter once", func() {
					Eventually(func() uint64 {
						return metricSender.GetCounter("StagingFailedToResolve")
					}).Should(Equal(uint64(1)))

					Consistently(func() uint64 {
						return metricSender.GetCounter("StagingFailedToResolve")
					}).Should(Equal(uint64(1)))
				})

				It("does not attempt to resolve the task", func() {
					Consistently(bbs.ResolveTaskCallCount).Should(Equal(0))
				})

				Context("when POSTing the staging-complete message succeeds the second time", func() {
					BeforeEach(func() {
						stagingCompleteResults := make(chan error, 2)
						stagingCompleteResults <- &api_client.BadResponseError{503}
						stagingCompleteResults <- nil

						fakeApiClient.StagingCompleteStub = func([]byte, lager.Logger) error {
							return <-stagingCompleteResults
						}
					})

					It("resolves the task", func() {
						Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
						Ω(bbs.ResolveTaskArgsForCall(0)).To(Equal(task.TaskGuid))
					})
				})
			})

			Context("when the api client error is not retryable", func() {
				BeforeEach(func() {
					fakeApiClient.StagingCompleteReturns(&api_client.BadResponseError{404})
				})

				It("resolves the task", func() {
					Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
					Ω(bbs.ResolveTaskArgsForCall(0)).To(Equal(task.TaskGuid))
				})
			})
		})

		Context("when resolving the task fails", func() {
			BeforeEach(func() {
				bbs.ResolvingTaskReturns(errors.New("oops"))
			})

			It("does not send a response to the requester, because another stager probably resolved it", func() {
				Consistently(fakeApiClient.StagingCompleteCallCount).Should(Equal(0))
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
			Consistently(fakeApiClient.StagingCompleteCallCount).Should(Equal(0))
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
				expectedBody = []byte(fmt.Sprintf(`{
					"execution_metadata":"{\"cmd\":\"./some-start-command\"}",
					"detected_start_command":{"web":"./some-start-command"},
					"app_id": "%s",
					"task_id": "%s"
				}`, appId, taskId))
			})

			It("resolves the completed task, publishes its result and then marks the Task as resolved", func() {
				Eventually(bbs.ResolvingTaskCallCount).Should(Equal(1))
				Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(1))
				payload, _ := fakeApiClient.StagingCompleteArgsForCall(0)
				Ω(payload).Should(MatchJSON(expectedBody))

				Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
				Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.TaskGuid))
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

			Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(1))
		})
	})

	Context("when a failed task appears in the outbox", func() {
		BeforeEach(func() {
			expectedBody = []byte(fmt.Sprintf(`{
				"app_id":"%s",
				"buildpack_key": "",
				"detected_buildpack": "",
				"execution_metadata": "",
				"detected_start_command":null,
				"error":"because i said so",
				"task_id":"%s"
			}`, appId, taskId))

			task.Failed = true
			task.FailureReason = "because i said so"
			completedTasks <- task
		})

		It("publishes its reason as an error and then marks the task as completed", func() {
			Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(1))
			payload, _ := fakeApiClient.StagingCompleteArgsForCall(0)
			Ω(payload).Should(MatchJSON(expectedBody))

			Eventually(bbs.ResolveTaskCallCount).Should(Equal(1))
			Ω(bbs.ResolveTaskArgsForCall(0)).Should(Equal(task.TaskGuid))
		})

		It("increments the staging failed counter", func() {
			Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(1))

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
		It("can accept new Completed Tasks before it's done processing existing tasks in the queue", func() {
			Eventually(completedTasks).Should(BeSent(task))
			Eventually(completedTasks).Should(BeSent(task))
			Eventually(completedTasks).Should(BeSent(task))

			Eventually(fakeApiClient.StagingCompleteCallCount).Should(Equal(3))
		})
	})
})
