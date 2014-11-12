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
		downloadTailorAction models.ExecutorAction
		runAction            models.ExecutorAction
		config               Config
		callbackURL          string
		backend              Backend
	)

	BeforeEach(func() {
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
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		backend = NewDockerBackend(config, logger)

		stagingRequest = cc_messages.DockerStagingRequestFromCC{
			AppId:           "bunny",
			TaskId:          "hop",
			DockerImageUrl:  "busybox",
			Stack:           "rabbit_hole",
			FileDescriptors: 512,
			MemoryMB:        2048,
			DiskMB:          3072,
			Environment: cc_messages.Environment{
				{"VCAP_APPLICATION", "foo"},
				{"VCAP_SERVICES", "bar"},
			},
		}

		downloadTailorAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     "http://file-server.com/v1/static/docker-circus.zip",
					To:       "/tmp/docker-circus",
					CacheKey: "tailor-docker",
				},
			},
			"",
			"",
			"Failed to Download Tailor",
		)

		fileDescriptorLimit := uint64(512)

		runAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.RunAction{
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
					Timeout: 15 * time.Minute,
					ResourceLimits: models.ResourceLimits{
						Nofile: &fileDescriptorLimit,
					},
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		)
	})

	JustBeforeEach(func() {
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
				stagingRequest.AppId = ""
			})
			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingAppId))
			})
		})

		Context("with a missing task id", func() {
			BeforeEach(func() {
				stagingRequest.TaskId = ""
			})
			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingTaskId))
			})
		})

		Context("with a missing docker image url", func() {
			BeforeEach(func() {
				stagingRequest.DockerImageUrl = ""
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
		Ω(desiredTask.LogSource).To(Equal("STG"))
		Ω(desiredTask.ResultFile).To(Equal("/tmp/docker-result/result.json"))

		var annotation models.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(models.StagingTaskAnnotation{
			AppId:  "bunny",
			TaskId: "hop",
		}))

		Ω(desiredTask.Actions).Should(HaveLen(2))

		Ω(desiredTask.Actions[0]).Should(Equal(downloadTailorAction))

		Ω(desiredTask.Actions[1]).Should(Equal(runAction))

		Ω(desiredTask.MemoryMB).To(Equal(2048))
		Ω(desiredTask.DiskMB).To(Equal(3072))
	})

	It("gives the task a callback URL to call it back", func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.CompletionCallbackURL).Should(Equal(callbackURL))
	})

	Describe("resource limits", func() {
		Context("when the app's memory limit is less than the minimum memory", func() {
			BeforeEach(func() {
				stagingRequest.MemoryMB = 256
			})

			It("uses the minimum memory", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.MemoryMB).Should(BeNumerically("==", config.MinMemoryMB))
			})
		})

		Context("when the app's disk limit is less than the minimum disk", func() {
			BeforeEach(func() {
				stagingRequest.DiskMB = 256
			})

			It("uses the minimum disk", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.DiskMB).Should(BeNumerically("==", config.MinDiskMB))
			})
		})

		Context("when the app's memory limit is less than the minimum memory", func() {
			BeforeEach(func() {
				stagingRequest.FileDescriptors = 17
			})

			It("uses the minimum file descriptors", func() {
				desiredTask, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).ShouldNot(HaveOccurred())

				Ω(desiredTask.Actions[1]).Should(Equal(models.EmitProgressFor(
					models.ExecutorAction{
						models.RunAction{
							Path: "/tmp/docker-circus/tailor",
							Args: []string{
								"-outputMetadataJSONFilename", "/tmp/docker-result/result.json",
								"-dockerRef", "busybox",
							},
							Env: []models.EnvironmentVariable{
								{"VCAP_APPLICATION", "foo"},
								{"VCAP_SERVICES", "bar"},
							},
							Timeout:        15 * time.Minute,
							ResourceLimits: models.ResourceLimits{Nofile: &config.MinFileDescriptors},
						},
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
						Error:  "fake-error-message",
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
								Error:  "some-failure-reason",
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
})
