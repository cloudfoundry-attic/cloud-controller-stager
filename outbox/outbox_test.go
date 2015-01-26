package outbox_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"net/http"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/backend/fake_backend"
	"github.com/cloudfoundry-incubator/stager/cc_client"
	"github.com/cloudfoundry-incubator/stager/cc_client/fakes"
	"github.com/cloudfoundry-incubator/stager/outbox"
	"github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/clock/fakeclock"
	"github.com/pivotal-golang/lager"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("Outbox", func() {
	var (
		logger lager.Logger
		appId  string
		taskId string

		runner  *outbox.Outbox
		process ifrit.Process

		fakeCCClient        *fakes.FakeCcClient
		fakeBackend         *fake_backend.FakeBackend
		backendResponse     []byte
		backendError        error
		fakeClock           *fakeclock.FakeClock
		metricSender        *fake.FakeMetricSender
		stagingDurationNano time.Duration

		outboxAddress string
	)

	BeforeEach(func() {
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		appId = "my_app_id"
		taskId = "do_this"

		outboxAddress = fmt.Sprintf("127.0.0.1:%d", 8888+GinkgoParallelNode())

		stagingDurationNano = 900900
		metricSender = fake.NewFakeMetricSender()
		metrics.Initialize(metricSender)

		fakeCCClient = &fakes.FakeCcClient{}
		fakeBackend = &fake_backend.FakeBackend{}
		fakeBackend.TaskDomainReturns("fake-domain")
		backendResponse = []byte("fake-response")
		backendError = nil

		fakeClock = fakeclock.NewFakeClock(time.Now())

		runner = outbox.New(outboxAddress, fakeCCClient, []backend.Backend{fakeBackend}, logger, fakeClock)
	})

	JustBeforeEach(func() {
		process = ifrit.Invoke(runner)
		fakeBackend.BuildStagingResponseReturns(backendResponse, backendError)
	})

	AfterEach(func() {
		process.Signal(syscall.SIGTERM)
		Eventually(process.Wait()).Should(Receive())
	})

	postTask := func(task receptor.TaskResponse) *http.Response {
		taskJSON, err := json.Marshal(task)
		Ω(err).ShouldNot(HaveOccurred())

		response, err := http.Post("http://"+outboxAddress, "application/json", bytes.NewReader(taskJSON))
		Ω(err).ShouldNot(HaveOccurred())

		return response
	}

	Context("when a staging task completes", func() {
		var resp *http.Response
		var taskResponse receptor.TaskResponse

		JustBeforeEach(func() {
			annotationJson, _ := json.Marshal(models.StagingTaskAnnotation{
				AppId:  appId,
				TaskId: taskId,
			})

			createdAt := fakeClock.Now().UnixNano()
			fakeClock.Increment(stagingDurationNano)

			taskResponse = receptor.TaskResponse{
				TaskGuid:  "the-task-guid",
				Domain:    "fake-domain",
				CreatedAt: createdAt,
				Result: `{
					"buildpack_key":"buildpack-key",
					"detected_buildpack":"Some Buildpack",
					"execution_metadata":"{\"start_command\":\"./some-start-command\"}",
					"detected_start_command":{"web":"./some-start-command"}
				}`,
				Action: &models.RunAction{
					Path: "ls",
				},
				Annotation: string(annotationJson),
			}
			resp = postTask(taskResponse)
		})

		It("passes the task response to the matching response builder", func() {
			Eventually(fakeBackend.BuildStagingResponseCallCount).Should(Equal(1))
			Ω(fakeBackend.BuildStagingResponseArgsForCall(0)).Should(Equal(taskResponse))
		})

		Context("when the response builder returns an error", func() {
			BeforeEach(func() {
				backendError = errors.New("build error")
			})

			It("returns a 400", func() {
				Ω(resp.StatusCode).Should(Equal(http.StatusBadRequest))
			})
		})

		Context("when the response builder does not return an error", func() {
			BeforeEach(func() {
				backendResponse = []byte("fake-response")
			})

			It("posts the response builder's result to CC", func() {
				Eventually(fakeCCClient.StagingCompleteCallCount).Should(Equal(1))
				payload, _ := fakeCCClient.StagingCompleteArgsForCall(0)
				Ω(payload).Should(Equal([]byte("fake-response")))
			})

			Context("when the CC request succeeds", func() {
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

				It("returns a 200", func() {
					Ω(resp.StatusCode).Should(Equal(200))
				})
			})

			Context("when the CC request fails", func() {
				BeforeEach(func() {
					fakeCCClient.StagingCompleteReturns(&cc_client.BadResponseError{504})
				})

				It("responds with the status code that the CC returned", func() {
					Ω(resp.StatusCode).Should(Equal(504))
				})
			})

			Context("When an error occurs in making the CC request", func() {
				BeforeEach(func() {
					fakeCCClient.StagingCompleteReturns(errors.New("whoops"))
				})

				It("responds with a 503 error", func() {
					Ω(resp.StatusCode).Should(Equal(503))
				})

				It("does not update the staging counter", func() {
					Consistently(func() uint64 {
						return metricSender.GetCounter("StagingRequestsSucceeded")
					}).Should(Equal(uint64(0)))
				})

				It("does not update the staging duration", func() {
					Consistently(func() fake.Metric {
						return metricSender.GetValue("StagingRequestSucceededDuration")
					}).Should(Equal(fake.Metric{}))
				})
			})
		})

		It("can accept new Completed Tasks before it's done processing existing tasks in the queue", func() {
			for i := 0; i < 3; i++ {
				postTask(receptor.TaskResponse{
					Action: &models.RunAction{
						Path: "ls",
					},
					TaskGuid:   fmt.Sprintf("task-guid-%d", i),
					Annotation: `{}`,
					Result:     `{}`,
					Domain:     "fake-domain",
				})
			}

			Eventually(fakeCCClient.StagingCompleteCallCount).Should(Equal(4))
		})
	})

	Context("when a staging task fails", func() {
		var resp *http.Response

		BeforeEach(func() {
			backendResponse = []byte("fake-response")
		})

		JustBeforeEach(func() {
			createdAt := fakeClock.Now().UnixNano()
			fakeClock.Increment(stagingDurationNano)

			resp = postTask(receptor.TaskResponse{
				Domain:        "fake-domain",
				Failed:        true,
				CreatedAt:     createdAt,
				FailureReason: "because I said so",
				Annotation: `{
					"task_id": "the-task-id",
					"app_id": "the-app-id"
				}`,
				Action: &models.RunAction{
					Path: "ls",
				},
				Result: `{}`,
			})
		})

		It("posts the result to CC as an error", func() {
			Eventually(fakeCCClient.StagingCompleteCallCount).Should(Equal(1))
			payload, _ := fakeCCClient.StagingCompleteArgsForCall(0)
			Ω(payload).Should(Equal([]byte("fake-response")))
		})

		It("responds with a 200", func() {
			Ω(resp.StatusCode).Should(Equal(http.StatusOK))
		})

		It("increments the staging failed counter", func() {
			Eventually(fakeCCClient.StagingCompleteCallCount).Should(Equal(1))
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

	Context("when a non-staging task is reported", func() {
		var resp *http.Response

		JustBeforeEach(func() {
			resp = postTask(receptor.TaskResponse{
				Action: &models.RunAction{
					Path: "ls",
				},
				Domain:        "some-other-crazy-domain",
				Failed:        true,
				FailureReason: "because I said so",
				Annotation:    `{}`,
				Result:        `{}`,
			})
		})

		It("responds with a 404", func() {
			Ω(resp.StatusCode).Should(Equal(404))
		})
	})

	Context("when invalid JSON is posted instead of a task", func() {
		var resp *http.Response

		JustBeforeEach(func() {
			var err error
			resp, err = http.Post("http://"+outboxAddress, "application/json", strings.NewReader("{"))
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("responds with a 400", func() {
			Ω(resp.StatusCode).Should(Equal(400))
		})
	})
})
