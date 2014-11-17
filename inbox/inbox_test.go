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
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/receptor/fake_receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/metric"
	"github.com/cloudfoundry-incubator/stager/backend/fake_backend"
	"github.com/cloudfoundry-incubator/stager/cc_client/fakes"
	. "github.com/cloudfoundry-incubator/stager/inbox"
	fake_metric_sender "github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/cloudfoundry/gunk/diegonats"
)

var _ = Describe("Inbox", func() {
	var fakenats *diegonats.FakeNATSClient
	var fakeCcClient *fakes.FakeCcClient
	var fakeBackend *fake_backend.FakeBackend
	var logOutput *gbytes.Buffer
	var logger lager.Logger
	var stagingRequestJson []byte
	var stopStagingRequestJson []byte
	var fakeDiegoClient *fake_receptor.FakeClient

	BeforeEach(func() {
		logOutput = gbytes.NewBuffer()
		logger = lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(logOutput, lager.INFO))

		stagingRequest := cc_messages.StagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}
		var err error
		stagingRequestJson, err = json.Marshal(stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())

		stopStagingRequest := cc_messages.StopStagingRequestFromCC{
			AppId:  "myapp",
			TaskId: "mytask",
		}
		stopStagingRequestJson, err = json.Marshal(stopStagingRequest)
		Ω(err).ShouldNot(HaveOccurred())

		fakenats = diegonats.NewFakeClient()
		fakeCcClient = &fakes.FakeCcClient{}
		fakeBackend = &fake_backend.FakeBackend{}
		fakeBackend.StagingRequestsNatsSubjectReturns("stage-subscription-subject")
		fakeBackend.StagingRequestsReceivedCounterReturns(metric.Counter("FakeStageMetricName"))
		fakeBackend.StopStagingRequestsNatsSubjectReturns("stop-subscription-subject")
		fakeBackend.StopStagingRequestsReceivedCounterReturns(metric.Counter("FakeStopMetricName"))
		fakeDiegoClient = &fake_receptor.FakeClient{}
	})

	publishStagingMessage := func() {
		fakenats.Publish("stage-subscription-subject", stagingRequestJson)
	}

	publishStopStagingMessage := func() {
		fakenats.Publish("stop-subscription-subject", stopStagingRequestJson)
	}

	Context("subscriptions", func() {
		var attempts uint32
		var process chan ifrit.Process

		JustBeforeEach(func() {
			process = make(chan ifrit.Process)
			go func() {
				process <- ifrit.Invoke(New(fakenats, fakeCcClient, fakeDiegoClient, fakeBackend, logger))
			}()
		})

		Context("when subscribing fails", func() {
			BeforeEach(func() {
				fakenats.WhenSubscribing("stage-subscription-subject", func(callback nats.MsgHandler) error {
					atomic.AddUint32(&attempts, 1)
					return errors.New("oh no!")
				})
			})

			AfterEach(func() {
				var inbox ifrit.Process
				Eventually(process).Should(Receive(&inbox))
				inbox.Signal(syscall.SIGTERM)
				Eventually(inbox.Wait()).Should(Receive())
			})

			It("continues retrying until it succeeds", func() {
				Eventually(func() uint32 {
					return atomic.LoadUint32(&attempts)
				}).Should(BeNumerically(">=", 1))

				Eventually(func() uint32 {
					return atomic.LoadUint32(&attempts)
				}).Should(BeNumerically(">=", 2))

				Consistently(func() []*nats.Subscription {
					return fakenats.Subscriptions("stage-subscription-subject")
				}).Should(BeEmpty())

				fakenats.WhenSubscribing("stage-subscription-subject", func(callback nats.MsgHandler) error {
					return nil
				})

				Eventually(func() []*nats.Subscription {
					return fakenats.Subscriptions("stage-subscription-subject")
				}).ShouldNot(BeEmpty())
			})
		})

		Context("when subscribing succeeds", func() {
			var inbox ifrit.Process

			JustBeforeEach(func() {
				Eventually(process).Should(Receive(&inbox))
			})

			AfterEach(func() {
				inbox.Signal(syscall.SIGTERM)
				Eventually(inbox.Wait()).Should(Receive())
			})

			It("subscribes to the staging start and stop subjects", func() {
				Ω(fakeBackend.StagingRequestsNatsSubjectCallCount()).Should(Equal(1))
				Ω(fakeBackend.StopStagingRequestsNatsSubjectCallCount()).Should(Equal(1))

				Ω(fakenats.Subscriptions("stage-subscription-subject")).Should(HaveLen(1))
				Ω(fakenats.Subscriptions("stop-subscription-subject")).Should(HaveLen(1))
			})

			Context("and it receives a staging request", func() {
				It("increments the counter to track arriving staging messages", func() {
					metricSender := fake_metric_sender.NewFakeMetricSender()
					metrics.Initialize(metricSender)
					publishStagingMessage()
					Ω(metricSender.GetCounter("FakeStageMetricName")).Should(Equal(uint64(1)))
				})

				It("builds a staging recipe", func() {
					publishStagingMessage()

					Ω(fakeBackend.BuildRecipeCallCount()).To(Equal(1))
					Ω(fakeBackend.BuildRecipeArgsForCall(0)).To(Equal(stagingRequestJson))
				})

				Context("when the recipe was built successfully", func() {
					var fakeTaskRequest = receptor.TaskCreateRequest{Annotation: "test annotation"}
					BeforeEach(func() {
						fakeBackend.BuildRecipeReturns(fakeTaskRequest, nil)
					})

					It("does not send a staging complete message", func() {
						publishStagingMessage()
						Ω(fakeCcClient.StagingCompleteCallCount()).To(Equal(0))
					})

					It("creates a task on Diego", func() {
						publishStagingMessage()
						Ω(fakeDiegoClient.CreateTaskCallCount()).To(Equal(1))
						Ω(fakeDiegoClient.CreateTaskArgsForCall(0)).To(Equal(fakeTaskRequest))
					})

					Context("create task does not fail", func() {
						It("does not send a staging failure response", func() {
							publishStagingMessage()

							Ω(fakeCcClient.StagingCompleteCallCount()).To(Equal(0))
						})
					})

					Context("when the task has already been created", func() {
						BeforeEach(func() {
							fakeDiegoClient.CreateTaskReturns(receptor.Error{
								Type:    receptor.TaskGuidAlreadyExists,
								Message: "ok, this task already exists",
							})
						})

						It("does not log a failure", func() {
							Ω(logOutput.Contents()).Should(BeEmpty())
							publishStagingMessage()
							Ω(logOutput).ShouldNot(gbytes.Say("staging-failed"))
						})
					})

					Context("create task fails for any other reason", func() {
						BeforeEach(func() {
							fakeDiegoClient.CreateTaskReturns(errors.New("create task error"))
						})

						It("logs the failure", func() {
							Ω(logOutput.Contents()).Should(BeEmpty())
							publishStagingMessage()
							Ω(logOutput).Should(gbytes.Say("staging-failed"))
						})

						It("sends a staging failure response", func() {
							publishStagingMessage()

							Ω(fakeCcClient.StagingCompleteCallCount()).To(Equal(1))
						})
					})
				})

				Context("when the recipe failed to be built", func() {
					BeforeEach(func() {
						fakeBackend.BuildRecipeReturns(receptor.TaskCreateRequest{}, errors.New("fake error"))
						responseForCC := cc_messages.StagingResponseForCC{Error: "some fake error"}
						responseForCCjson, err := json.Marshal(responseForCC)
						Ω(err).ShouldNot(HaveOccurred())
						fakeBackend.BuildStagingResponseFromRequestErrorReturns(responseForCCjson, nil)
					})

					It("logs the failure", func() {
						Ω(logOutput.Contents()).Should(BeEmpty())

						publishStagingMessage()

						Ω(logOutput).Should(gbytes.Say("recipe-building-failed"))
					})

					It("sends a staging failure response", func() {
						publishStagingMessage()

						Ω(fakeCcClient.StagingCompleteCallCount()).To(Equal(1))
						response, _ := fakeCcClient.StagingCompleteArgsForCall(0)

						stagingResponse := cc_messages.StagingResponseForCC{}
						json.Unmarshal(response, &stagingResponse)
						Ω(stagingResponse.Error).Should(Equal("some fake error"))
					})

					Context("when the response builder fails", func() {
						BeforeEach(func() {
							fakeBackend.BuildStagingResponseFromRequestErrorReturns(nil, errors.New("builder error"))
						})

						It("does not send a message in response", func() {
							publishStagingMessage()
							Ω(fakeCcClient.StagingCompleteCallCount()).To(Equal(0))
						})
					})
				})
			})

			Context("and it receives a stop staging request", func() {
				It("increments the counter to track arriving stop staging messages", func() {
					metricSender := fake_metric_sender.NewFakeMetricSender()
					metrics.Initialize(metricSender)
					publishStopStagingMessage()
					Ω(metricSender.GetCounter("FakeStopMetricName")).Should(Equal(uint64(1)))
				})

				It("builds a stop staging recipe", func() {
					publishStopStagingMessage()

					Ω(fakeBackend.StagingTaskGuidCallCount()).To(Equal(1))
					Ω(fakeBackend.StagingTaskGuidArgsForCall(0)).To(Equal(stopStagingRequestJson))
				})

				Context("when the task guid was built successfully", func() {
					var taskGuid = "task-guid"
					BeforeEach(func() {
						fakeBackend.StagingTaskGuidReturns(taskGuid, nil)
					})

					It("cancels a task on Diego", func() {
						publishStopStagingMessage()
						Ω(fakeDiegoClient.CancelTaskCallCount()).To(Equal(1))
						Ω(fakeDiegoClient.CancelTaskArgsForCall(0)).To(Equal(taskGuid))
					})
				})

				Context("when the staging task guid fails to be built", func() {
					BeforeEach(func() {
						fakeBackend.StagingTaskGuidReturns("", errors.New("fake error"))
					})

					It("logs the failure", func() {
						Ω(logOutput.Contents()).Should(BeEmpty())

						publishStopStagingMessage()

						Ω(logOutput).Should(gbytes.Say("staging-task-guid-faile"))
					})
				})
			})
		})
	})
})
