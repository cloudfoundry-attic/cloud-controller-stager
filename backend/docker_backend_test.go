package backend_test

import (
	"encoding/json"
	"time"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {
	var (
		stagingRequest       cc_messages.DockerStagingRequestFromCC
		stagingRequestJson   []byte
		downloadTailorAction models.Action
		runAction            models.Action
		config               Config
		callbackURL          string
		backend              Backend

		appId           string
		taskId          string
		dockerImageUrl  string
		fileDescriptors int
		memoryMB        int
		diskMB          int
		timeout         int
	)

	BeforeEach(func() {
		appId = "bunny"
		taskId = "hop"
		dockerImageUrl = "busybox"
		fileDescriptors = 512
		memoryMB = 2048
		diskMB = 3072
		timeout = 900

		callbackURL = "http://the-stager.example.com"

		config = Config{
			CallbackURL:   callbackURL,
			FileServerURL: "http://file-server.com",
			Circuses: map[string]string{
				"penguin":                "penguin-compiler",
				"rabbit_hole":            "rabbit-hole-compiler",
				"compiler_with_full_url": "http://the-full-compiler-url",
				"compiler_with_bad_url":  "ftp://the-bad-compiler-url",
			},
			MinDiskMB:          2048,
			MinMemoryMB:        1024,
			MinFileDescriptors: 256,
			Sanitizer:          func(msg string) string { return msg + " was totally sanitized" },
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		backend = NewDockerBackend(config, logger)

		downloadTailorAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker-circus.zip",
				To:       "/tmp/docker-circus",
				CacheKey: "tailor-docker",
			},
			"",
			"",
			"Failed to set up docker environment",
		)

		fileDescriptorLimit := uint64(512)

		runAction = models.EmitProgressFor(
			&models.RunAction{
				Path: "/tmp/docker-circus/tailor",
				Args: []string{
					"-outputMetadataJSONFilename",
					"/tmp/docker-result/result.json",
					"-dockerRef",
					"busybox",
				},
				Env: []models.EnvironmentVariable{
					{
						Name:  "VCAP_APPLICATION",
						Value: "foo",
					},
					{
						Name:  "VCAP_SERVICES",
						Value: "bar",
					},
				},
				ResourceLimits: models.ResourceLimits{
					Nofile: &fileDescriptorLimit,
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		)
	})

	JustBeforeEach(func() {
		stagingRequest = cc_messages.DockerStagingRequestFromCC{
			AppId:           appId,
			TaskId:          taskId,
			DockerImageUrl:  dockerImageUrl,
			Stack:           "rabbit_hole",
			FileDescriptors: fileDescriptors,
			MemoryMB:        memoryMB,
			DiskMB:          diskMB,
			Environment: cc_messages.Environment{
				{"VCAP_APPLICATION", "foo"},
				{"VCAP_SERVICES", "bar"},
			},
			Timeout: timeout,
		}

		var err error
		stagingRequestJson, err = json.Marshal(stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())
	})

	Describe("request validation", func() {
		Context("with invalid request JSON", func() {
			JustBeforeEach(func() {
				stagingRequestJson = []byte("bad-json")
			})
			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(HaveOccurred())
				Ω(err).Should(BeAssignableToTypeOf(&json.SyntaxError{}))
			})
		})
		Context("with a missing app id", func() {
			BeforeEach(func() {
				appId = ""
			})

			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingAppId))
			})
		})

		Context("with a missing task id", func() {
			BeforeEach(func() {
				taskId = ""
			})

			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingTaskId))
			})
		})

		Context("with a missing docker image url", func() {
			BeforeEach(func() {
				dockerImageUrl = ""
			})

			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingDockerImageUrl))
			})
		})
	})

	It("creates a cf-app-docker-staging Task with staging instructions", func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.Domain).To(Equal("cf-app-docker-staging"))
		Ω(desiredTask.TaskGuid).To(Equal("bunny-hop"))
		Ω(desiredTask.Stack).To(Equal("rabbit_hole"))
		Ω(desiredTask.LogGuid).To(Equal("bunny"))
		Ω(desiredTask.LogSource).To(Equal(TaskLogSource))
		Ω(desiredTask.ResultFile).To(Equal("/tmp/docker-result/result.json"))

		var annotation models.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(models.StagingTaskAnnotation{
			AppId:  "bunny",
			TaskId: "hop",
		}))

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(HaveLen(2))
		Ω(actions[0]).Should(Equal(downloadTailorAction))
		Ω(actions[1]).Should(Equal(runAction))

		Ω(desiredTask.MemoryMB).To(Equal(2048))
		Ω(desiredTask.DiskMB).To(Equal(3072))
	})

	It("gives the task a callback URL to call it back", func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.CompletionCallbackURL).Should(Equal(callbackURL))
	})

	Describe("staging action timeout", func() {
		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(time.Duration(timeout) * time.Second))
			})
		})

		Context("when a 0 timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 0
			})

			It("uses the default timeout", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(DefaultStagingTimeout))
			})
		})

		Context("when a negative timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = -3
			})

			It("uses the default timeout", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(DefaultStagingTimeout))
			})
		})
	})

	Describe("resource limits", func() {
		Context("when the app's memory limit is less than the minimum memory", func() {
			BeforeEach(func() {
				memoryMB = 256
			})

			It("uses the minimum memory", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.MemoryMB).Should(BeNumerically("==", config.MinMemoryMB))
			})
		})

		Context("when the app's disk limit is less than the minimum disk", func() {
			BeforeEach(func() {
				diskMB = 256
			})

			It("uses the minimum disk", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.DiskMB).Should(BeNumerically("==", config.MinDiskMB))
			})
		})

		Context("when the app's memory limit is less than the minimum memory", func() {
			BeforeEach(func() {
				fileDescriptors = 17
			})

			It("uses the minimum file descriptors", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(actionsFromDesiredTask(desiredTask)[1]).Should(Equal(models.EmitProgressFor(
					&models.RunAction{
						Path: "/tmp/docker-circus/tailor",
						Args: []string{
							"-outputMetadataJSONFilename", "/tmp/docker-result/result.json",
							"-dockerRef", "busybox",
						},
						Env: []models.EnvironmentVariable{
							{"VCAP_APPLICATION", "foo"},
							{"VCAP_SERVICES", "bar"},
						},
						ResourceLimits: models.ResourceLimits{Nofile: &config.MinFileDescriptors},
					},
					"Staging...",
					"Staging Complete",
					"Staging Failed",
				)))
			})
		})
	})

	Describe("building staging responses", func() {
		var buildError error
		var responseJson []byte

		Describe("BuildStagingResponseFromRequestError", func() {
			var requestJson []byte

			JustBeforeEach(func() {
				responseJson, buildError = backend.BuildStagingResponseFromRequestError(requestJson, "fake-error-message")
			})

			Context("with a valid request", func() {
				BeforeEach(func() {
					request := cc_messages.DockerStagingRequestFromCC{
						AppId:  "myapp",
						TaskId: "mytask",
					}
					var err error
					requestJson, err = json.Marshal(request)
					Ω(err).ShouldNot(HaveOccurred())
				})

				It("returns a correctly populated staging response", func() {
					expectedResponse := cc_messages.DockerStagingResponseForCC{
						AppId:  "myapp",
						TaskId: "mytask",
						Error:  "fake-error-message was totally sanitized",
					}
					expectedResponseJson, err := json.Marshal(expectedResponse)
					Ω(err).ShouldNot(HaveOccurred())

					Ω(buildError).ShouldNot(HaveOccurred())
					Ω(responseJson).Should(MatchJSON(expectedResponseJson))
				})
			})

			Context("with an invalid request", func() {
				BeforeEach(func() {
					requestJson = []byte("invalid-json")
				})

				It("returns an error", func() {
					Ω(buildError).Should(HaveOccurred())
					Ω(buildError).Should(BeAssignableToTypeOf(&json.SyntaxError{}))
					Ω(responseJson).Should(BeNil())
				})
			})
		})

		Describe("BuildStagingResponse", func() {
			var annotationJson []byte
			var stagingResultJson []byte
			var taskResponseFailed bool
			var failureReason string

			JustBeforeEach(func() {
				taskResponse := receptor.TaskResponse{
					Annotation:    string(annotationJson),
					Failed:        taskResponseFailed,
					FailureReason: failureReason,
					Result:        string(stagingResultJson),
				}
				responseJson, buildError = backend.BuildStagingResponse(taskResponse)
			})

			Context("with a valid annotation", func() {
				BeforeEach(func() {
					annotation := models.StagingTaskAnnotation{
						AppId:  "app-id",
						TaskId: "task-id",
					}
					var err error
					annotationJson, err = json.Marshal(annotation)
					Ω(err).ShouldNot(HaveOccurred())
				})

				Context("with a successful task response", func() {
					BeforeEach(func() {
						taskResponseFailed = false
					})

					Context("with a valid staging result", func() {
						BeforeEach(func() {
							stagingResult := models.StagingDockerResult{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
							}
							var err error
							stagingResultJson, err = json.Marshal(stagingResult)
							Ω(err).ShouldNot(HaveOccurred())
						})

						It("populates a staging response correctly", func() {
							expectedResponse := cc_messages.DockerStagingResponseForCC{
								AppId:                "app-id",
								TaskId:               "task-id",
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
							}
							expectedResponseJson, err := json.Marshal(expectedResponse)
							Ω(err).ShouldNot(HaveOccurred())

							Ω(buildError).ShouldNot(HaveOccurred())
							Ω(responseJson).Should(MatchJSON(expectedResponseJson))
						})
					})

					Context("with an invalid staging result", func() {
						BeforeEach(func() {
							stagingResultJson = []byte("invalid-json")
						})

						It("returns an error", func() {
							Ω(buildError).Should(HaveOccurred())
							Ω(buildError).Should(BeAssignableToTypeOf(&json.SyntaxError{}))
							Ω(responseJson).Should(BeNil())
						})
					})

					Context("with a failed task response", func() {
						BeforeEach(func() {
							taskResponseFailed = true
							failureReason = "some-failure-reason"
						})

						It("populates a staging response correctly", func() {
							expectedResponse := cc_messages.DockerStagingResponseForCC{
								AppId:  "app-id",
								TaskId: "task-id",
								Error:  "some-failure-reason was totally sanitized",
							}
							expectedResponseJson, err := json.Marshal(expectedResponse)
							Ω(err).ShouldNot(HaveOccurred())

							Ω(buildError).ShouldNot(HaveOccurred())
							Ω(responseJson).Should(MatchJSON(expectedResponseJson))
						})
					})
				})
			})

			Context("with an invalid annotation", func() {
				BeforeEach(func() {
					annotationJson = []byte("invalid-json")
				})

				It("returns an error", func() {
					Ω(buildError).Should(HaveOccurred())
					Ω(buildError).Should(BeAssignableToTypeOf(&json.SyntaxError{}))
					Ω(responseJson).Should(BeNil())
				})
			})
		})
	})

	Describe("StagingTaskGuid", func() {
		It("returns the staging task guid", func() {
			taskGuid, err := backend.StagingTaskGuid(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())
			Ω(taskGuid).Should(Equal("bunny-hop"))
		})

		It("matches the task guid on the TaskRequest from BuildRecipe", func() {
			taskGuid, _ := backend.StagingTaskGuid(stagingRequestJson)
			desiredTask, _ := backend.BuildRecipe(stagingRequestJson)

			Ω(taskGuid).Should(Equal(desiredTask.TaskGuid))
		})

		It("fails if the AppId is missing", func() {
			_, err := backend.StagingTaskGuid([]byte(`{"task_id":"hop"}`))
			Ω(err).Should(Equal(ErrMissingAppId))
		})

		It("fails if the TaskId is missing", func() {
			_, err := backend.StagingTaskGuid([]byte(`{"app_id":"bunny"}`))
			Ω(err).Should(Equal(ErrMissingTaskId))
		})
	})
})
