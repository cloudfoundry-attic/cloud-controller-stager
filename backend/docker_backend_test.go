package backend_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/docker_app_lifecycle"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/backend"
	"github.com/cloudfoundry-incubator/stager/helpers"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("DockerBackend", func() {
	var (
		stagingRequest        cc_messages.StagingRequestFromCC
		downloadBuilderAction models.ActionInterface
		runAction             models.ActionInterface
		config                backend.Config
		logger                lager.Logger
		docker                backend.Backend

		stagingGuid       string
		appId             string
		dockerImageUrl    string
		dockerLoginServer string
		dockerUser        string
		dockerPassword    string
		dockerEmail       string
		fileDescriptors   int
		memoryMb          int32
		diskMb            int32
		timeout           int
		egressRules       []*models.SecurityGroupRule
	)

	BeforeEach(func() {
		appId = "bunny"
		dockerImageUrl = "busybox"
		dockerLoginServer = ""
		dockerUser = ""
		dockerPassword = ""
		dockerEmail = ""
		fileDescriptors = 512
		memoryMb = 2048
		diskMb = 3072
		timeout = 900

		stagingGuid = "a-staging-guid"

		stagerURL := "http://the-stager.example.com"

		config = backend.Config{
			TaskDomain:         "config-task-domain",
			StagerURL:          stagerURL,
			FileServerURL:      "http://file-server.com",
			CCUploaderURL:      "http://cc-uploader.com",
			DockerStagingStack: "penguin",
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

		downloadBuilderAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker_lifecycle/docker_app_lifecycle.tgz",
				To:       "/tmp/docker_app_lifecycle",
				CacheKey: "docker-lifecycle",
				User:     "vcap",
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
				Env: []*models.EnvironmentVariable{
					{
						Name:  "VCAP_APPLICATION",
						Value: "foo",
					},
					{
						Name:  "VCAP_SERVICES",
						Value: "bar",
					},
				},
				ResourceLimits: &models.ResourceLimits{
					Nofile: &fileDescriptorLimit,
				},
				User: "vcap",
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		)

		egressRules = []*models.SecurityGroupRule{
			{
				Protocol:     "TCP",
				Destinations: []string{"0.0.0.0/0"},
				PortRange:    &models.PortRange{Start: 80, End: 443},
			},
		}
	})

	JustBeforeEach(func() {
		logger = lagertest.NewTestLogger("test")
		docker = backend.NewDockerBackend(config, logger)

		rawJsonBytes, err := json.Marshal(cc_messages.DockerStagingData{
			DockerImageUrl:    dockerImageUrl,
			DockerLoginServer: dockerLoginServer,
			DockerUser:        dockerUser,
			DockerPassword:    dockerPassword,
			DockerEmail:       dockerEmail,
		})
		Expect(err).NotTo(HaveOccurred())
		lifecycleData := json.RawMessage(rawJsonBytes)

		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:           appId,
			LogGuid:         "log-guid",
			FileDescriptors: fileDescriptors,
			MemoryMB:        int(memoryMb),
			DiskMB:          int(diskMb),
			Environment: []*models.EnvironmentVariable{
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
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingDockerImageUrl))
			})
		})

		Context("with a missing credentials set", func() {
			Context("with missing user", func() {
				BeforeEach(func() {
					dockerPassword = "password"
					dockerEmail = "email@example.com"
				})

				It("returns an error", func() {
					_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
				})
			})

			Context("with missing password", func() {
				BeforeEach(func() {
					dockerUser = "user"
					dockerEmail = "email@example.com"
				})

				It("returns an error", func() {
					_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
				})

			})

			Context("with missing email", func() {
				BeforeEach(func() {
					dockerUser = "user"
					dockerPassword = "password"
				})

				It("returns an error", func() {
					_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
					Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
				})
			})

		})
	})

	Describe("docker lifeycle config", func() {
		Context("when the docker lifecycle is missing", func() {
			BeforeEach(func() {
				delete(config.Lifecycles, "docker")
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(Equal(backend.ErrNoCompilerDefined))
			})
		})

		Context("when the docker lifecycle is empty", func() {
			BeforeEach(func() {
				config.Lifecycles["docker"] = ""
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(Equal(backend.ErrNoCompilerDefined))
			})
		})

		Context("with invalid docker registry address", func() {
			BeforeEach(func() {
				config.DockerRegistryAddress = "://host:"
			})

			JustBeforeEach(func() {
				stagingRequest.Environment = []*models.EnvironmentVariable{
					{Name: "DIEGO_DOCKER_CACHE", Value: "true"},
				}
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(Equal(backend.ErrInvalidDockerRegistryAddress))
				Expect(logger).To(gbytes.Say(`{"address":"://host:","app-id":"bunny","error":"too many colons in address ://host:"`))
			})
		})

	})

	It("creates a cf-app-docker-staging Task with staging instructions", func() {
		taskDef, guid, domain, err := docker.BuildRecipe(stagingGuid, stagingRequest)
		Expect(err).NotTo(HaveOccurred())

		Expect(domain).To(Equal("config-task-domain"))
		Expect(guid).To(Equal(stagingGuid))
		Expect(taskDef.LogGuid).To(Equal("log-guid"))
		Expect(taskDef.LogSource).To(Equal(backend.TaskLogSource))
		Expect(taskDef.ResultFile).To(Equal("/tmp/docker-result/result.json"))
		Expect(taskDef.Privileged).To(BeTrue())

		var annotation cc_messages.StagingTaskAnnotation

		err = json.Unmarshal([]byte(taskDef.Annotation), &annotation)
		Expect(err).NotTo(HaveOccurred())

		Expect(annotation).To(Equal(cc_messages.StagingTaskAnnotation{
			Lifecycle: "docker",
		}))

		actions := actionsFromTaskDef(taskDef)
		Expect(actions).To(HaveLen(2))
		Expect(actions[0].GetEmitProgressAction()).To(Equal(downloadBuilderAction))
		Expect(actions[1].GetEmitProgressAction()).To(Equal(runAction))

		Expect(taskDef.MemoryMb).To(Equal(memoryMb))
		Expect(taskDef.DiskMb).To(Equal(diskMb))
		Expect(taskDef.EgressRules).To(ConsistOf(egressRules))
	})

	It("uses the configured docker staging stack", func() {
		taskDef, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
		Expect(err).NotTo(HaveOccurred())

		Expect(taskDef.RootFs).To(Equal(models.PreloadedRootFS("penguin")))
	})

	It("gives the task a callback URL to call it back", func() {
		taskDef, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
		Expect(err).NotTo(HaveOccurred())

		Expect(taskDef.CompletionCallbackUrl).To(Equal(fmt.Sprintf("%s/v1/staging/%s/completed", config.StagerURL, stagingGuid)))
	})

	Describe("staging action timeout", func() {
		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				taskDef, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := taskDef.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(time.Duration(timeout) * time.Second)))
			})
		})

		Context("when a 0 timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 0
			})

			It("uses the default timeout", func() {
				taskDef, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := taskDef.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(backend.DefaultStagingTimeout)))
			})
		})

		Context("when a negative timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = -3
			})

			It("uses the default timeout", func() {
				taskDef, _, _, err := docker.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := taskDef.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(backend.DefaultStagingTimeout)))
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
				taskResponse := &models.TaskCallbackResponse{
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
					Expect(err).NotTo(HaveOccurred())
				})

				Context("with a successful task response", func() {
					BeforeEach(func() {
						taskResponseFailed = false
					})

					Context("with a valid staging result", func() {
						const dockerImage = "cloudfoundry/diego-docker-app"
						var lifecycleData *json.RawMessage

						BeforeEach(func() {
							stagingResult := docker_app_lifecycle.StagingDockerResult{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
								DockerImage:          dockerImage,
							}
							var err error
							stagingResultJson, err = json.Marshal(stagingResult)
							Expect(err).NotTo(HaveOccurred())

							lifecycleData, err = helpers.BuildDockerStagingData(dockerImage)
							Expect(err).NotTo(HaveOccurred())
						})

						It("populates a staging response correctly", func() {
							Expect(buildError).NotTo(HaveOccurred())
							Expect(response).To(Equal(cc_messages.StagingResponseForCC{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
								LifecycleData:        lifecycleData,
							}))
						})
					})

					Context("with an invalid staging result", func() {
						BeforeEach(func() {
							stagingResultJson = []byte("invalid-json")
						})

						It("returns an error", func() {
							Expect(buildError).To(HaveOccurred())
							Expect(buildError).To(BeAssignableToTypeOf(&json.SyntaxError{}))
						})
					})

					Context("with a failed task response", func() {
						BeforeEach(func() {
							taskResponseFailed = true
							failureReason = "some-failure-reason"
						})

						It("populates a staging response correctly", func() {
							Expect(buildError).NotTo(HaveOccurred())
							Expect(response).To(Equal(cc_messages.StagingResponseForCC{
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
					Expect(buildError).To(HaveOccurred())
					Expect(buildError).To(BeAssignableToTypeOf(&json.SyntaxError{}))
				})
			})
		})
	})
})
