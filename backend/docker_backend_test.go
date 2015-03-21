package backend_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudfoundry-incubator/docker_app_lifecycle"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("DockerBackend", func() {
	var (
		stagingRequest        cc_messages.StagingRequestFromCC
		downloadBuilderAction models.Action
		runAction             models.Action
		config                backend.Config
		docker                backend.Backend

		stagingGuid     string
		appId           string
		dockerImageUrl  string
		fileDescriptors int
		memoryMB        int
		diskMB          int
		timeout         int
		egressRules     []models.SecurityGroupRule
	)

	BeforeEach(func() {
		appId = "bunny"
		dockerImageUrl = "busybox"
		fileDescriptors = 512
		memoryMB = 2048
		diskMB = 3072
		timeout = 900

		stagingGuid = "a-staging-guid"

		stagerURL := "http://the-stager.example.com"

		config = backend.Config{
			TaskDomain:    "config-task-domain",
			StagerURL:     stagerURL,
			FileServerURL: "http://file-server.com",
			Lifecycles: map[string]string{
				"penguin":                "penguin-compiler",
				"rabbit_hole":            "rabbit-hole-compiler",
				"compiler_with_full_url": "http://the-full-compiler-url",
				"compiler_with_bad_url":  "ftp://the-bad-compiler-url",
				"docker":                 "docker_lifecycle/docker_app_lifecycle.tgz",
			},
			Sanitizer: func(msg string) *cc_messages.StagingError {
				return &cc_messages.StagingError{Message: msg + " was totally sanitized"}
			},
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		docker = backend.NewDockerBackend(config, logger)

		downloadBuilderAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker_lifecycle/docker_app_lifecycle.tgz",
				To:       "/tmp/docker_app_lifecycle",
				CacheKey: "builder-docker",
			},
			"",
			"",
			"Failed to set up docker environment",
		)

		fileDescriptorLimit := uint64(512)

		runAction = models.EmitProgressFor(
			&models.RunAction{
				Path: "/tmp/docker_app_lifecycle/builder",
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

		egressRules = []models.SecurityGroupRule{
			{
				Protocol:     "TCP",
				Destinations: []string{"0.0.0.0/0"},
				PortRange:    &models.PortRange{Start: 80, End: 443},
			},
		}
	})

	JustBeforeEach(func() {
		dockerStagingData := cc_messages.DockerStagingData{
			DockerImageUrl: dockerImageUrl,
		}
		lifecycleDataJSON, err := json.Marshal(dockerStagingData)
		Ω(err).ShouldNot(HaveOccurred())

		lifecycleData := json.RawMessage(lifecycleDataJSON)

		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:           appId,
			LogGuid:         "log-guid",
			Stack:           "rabbit_hole",
			FileDescriptors: fileDescriptors,
			MemoryMB:        memoryMB,
			DiskMB:          diskMB,
			Environment: cc_messages.Environment{
				{"VCAP_APPLICATION", "foo"},
				{"VCAP_SERVICES", "bar"},
			},
			EgressRules:   egressRules,
			Timeout:       timeout,
			Lifecycle:     "docker",
			LifecycleData: &lifecycleData,
		}
	})

	Describe("request validation", func() {
		Context("with a missing docker image url", func() {
			BeforeEach(func() {
				dockerImageUrl = ""
			})

			It("returns an error", func() {
				_, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).Should(Equal(backend.ErrMissingDockerImageUrl))
			})
		})
	})

	Describe("docker lifeycle config", func() {
		Context("when the docker lifecycle is missing", func() {
			BeforeEach(func() {
				delete(config.Lifecycles, "docker")
			})

			It("returns an error", func() {
				_, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).Should(Equal(backend.ErrNoCompilerDefined))
			})
		})

		Context("when the docker lifecycle is empty", func() {
			BeforeEach(func() {
				config.Lifecycles["docker"] = ""
			})

			It("returns an error", func() {
				_, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).Should(Equal(backend.ErrNoCompilerDefined))
			})
		})
	})

	It("creates a cf-app-docker-staging Task with staging instructions", func() {
		desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.Domain).To(Equal("config-task-domain"))
		Ω(desiredTask.TaskGuid).To(Equal(stagingGuid))
		Ω(desiredTask.Stack).To(Equal("rabbit_hole"))
		Ω(desiredTask.LogGuid).To(Equal("log-guid"))
		Ω(desiredTask.LogSource).To(Equal(backend.TaskLogSource))
		Ω(desiredTask.ResultFile).To(Equal("/tmp/docker-result/result.json"))
		Ω(desiredTask.Privileged).Should(BeFalse())

		var annotation cc_messages.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(cc_messages.StagingTaskAnnotation{
			Lifecycle: "docker",
		}))

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(HaveLen(2))
		Ω(actions[0]).Should(Equal(downloadBuilderAction))
		Ω(actions[1]).Should(Equal(runAction))

		Ω(desiredTask.MemoryMB).To(Equal(memoryMB))
		Ω(desiredTask.DiskMB).To(Equal(diskMB))
		Ω(desiredTask.EgressRules).Should(ConsistOf(egressRules))
	})

	It("gives the task a callback URL to call it back", func() {
		desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.CompletionCallbackURL).Should(Equal(fmt.Sprintf("%s/v1/staging/%s/completed", config.StagerURL, stagingGuid)))
	})

	Describe("staging action timeout", func() {
		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
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
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(backend.DefaultStagingTimeout))
			})
		})

		Context("when a negative timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = -3
			})

			It("uses the default timeout", func() {
				desiredTask, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(backend.DefaultStagingTimeout))
			})
		})
	})

	Describe("building staging responses", func() {
		var response cc_messages.StagingResponseForCC

		Describe("BuildStagingResponse", func() {
			var annotationJson []byte
			var stagingResultJson []byte
			var taskResponseFailed bool
			var failureReason string
			var buildError error

			JustBeforeEach(func() {
				taskResponse := receptor.TaskResponse{
					Annotation:    string(annotationJson),
					Failed:        taskResponseFailed,
					FailureReason: failureReason,
					Result:        string(stagingResultJson),
				}

				response, buildError = docker.BuildStagingResponse(taskResponse)
			})

			Context("with a valid annotation", func() {
				BeforeEach(func() {
					annotation := cc_messages.StagingTaskAnnotation{
						Lifecycle: "docker",
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
							stagingResult := docker_app_lifecycle.StagingDockerResult{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
							}
							var err error
							stagingResultJson, err = json.Marshal(stagingResult)
							Ω(err).ShouldNot(HaveOccurred())
						})

						It("populates a staging response correctly", func() {
							Ω(buildError).ShouldNot(HaveOccurred())
							Ω(response).Should(Equal(cc_messages.StagingResponseForCC{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
							}))
						})
					})

					Context("with an invalid staging result", func() {
						BeforeEach(func() {
							stagingResultJson = []byte("invalid-json")
						})

						It("returns an error", func() {
							Ω(buildError).Should(HaveOccurred())
							Ω(buildError).Should(BeAssignableToTypeOf(&json.SyntaxError{}))
						})
					})

					Context("with a failed task response", func() {
						BeforeEach(func() {
							taskResponseFailed = true
							failureReason = "some-failure-reason"
						})

						It("populates a staging response correctly", func() {
							Ω(buildError).ShouldNot(HaveOccurred())
							Ω(response).Should(Equal(cc_messages.StagingResponseForCC{
								Error: &cc_messages.StagingError{Message: "some-failure-reason was totally sanitized"},
							}))
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
				})
			})
		})
	})
})
