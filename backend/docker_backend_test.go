package backend_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/docker_app_lifecycle"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("DockerBackend", func() {
	var (
		docker backend.Backend
		logger lager.Logger
		config backend.Config
	)

	BeforeEach(func() {
		config = backend.Config{
			TaskDomain:         "config-task-domain",
			StagerURL:          "http://staging-url.com",
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

		logger = lagertest.NewTestLogger("test")
		docker = backend.NewDockerBackend(config, logger)
	})

	Describe("BuildBackend", func() {
		var (
			stagingRequest cc_messages.StagingRequestFromCC
			dockerImageUrl string
			dockerUser     string
			dockerPassword string
			dockerEmail    string
			memoryMb       int32
			diskMb         int32
			timeout        int
		)

		BeforeEach(func() {
			dockerImageUrl = "busybox"
			memoryMb = 2048
			diskMb = 3072
			timeout = 900
		})

		AfterEach(func() {
			dockerUser = ""
			dockerPassword = ""
			dockerEmail = ""
		})

		JustBeforeEach(func() {
			rawJsonBytes, err := json.Marshal(cc_messages.DockerStagingData{
				DockerImageUrl:    dockerImageUrl,
				DockerLoginServer: "",
				DockerUser:        dockerUser,
				DockerPassword:    dockerPassword,
				DockerEmail:       dockerEmail,
			})
			Expect(err).NotTo(HaveOccurred())
			lifecycleData := json.RawMessage(rawJsonBytes)

			stagingRequest = cc_messages.StagingRequestFromCC{
				AppId:           "app-id",
				LogGuid:         "log-guid",
				FileDescriptors: 512,
				MemoryMB:        int(memoryMb),
				DiskMB:          int(diskMb),
				Environment: []*models.EnvironmentVariable{
					{"VCAP_APPLICATION", "foo"},
					{"VCAP_SERVICES", "bar"},
				},
				EgressRules: []*models.SecurityGroupRule{
					{
						Protocol:     "TCP",
						Destinations: []string{"0.0.0.0/0"},
						PortRange:    &models.PortRange{Start: 80, End: 443},
					},
				},
				Timeout:       timeout,
				Lifecycle:     "docker",
				LifecycleData: &lifecycleData,
			}
		})

		Context("with a missing docker image url", func() {
			BeforeEach(func() {
				dockerImageUrl = ""
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingDockerImageUrl))
			})
		})

		Context("with missing user", func() {
			BeforeEach(func() {
				dockerPassword = "password"
				dockerEmail = "email@example.com"
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
			})
		})

		Context("with missing password", func() {
			BeforeEach(func() {
				dockerUser = "user"
				dockerEmail = "email@example.com"
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
			})
		})

		Context("with missing email", func() {
			BeforeEach(func() {
				dockerUser = "user"
				dockerPassword = "password"
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingDockerCredentials))
			})
		})

		Context("when the docker lifecycle is missing", func() {
			BeforeEach(func() {
				delete(config.Lifecycles, "docker")
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrNoCompilerDefined))
			})
		})

		Context("when the docker lifecycle is empty", func() {
			BeforeEach(func() {
				config.Lifecycles["docker"] = ""
			})

			It("returns an error", func() {
				_, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).To(Equal(backend.ErrNoCompilerDefined))
			})
		})

		It("creates a cf-app-docker-staging Task with staging instructions", func() {
			taskDef, guid, domain, err := docker.BuildRecipe("staging-guid", stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(domain).To(Equal("config-task-domain"))
			Expect(guid).To(Equal("staging-guid"))
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

			dockerDownloadAction := models.EmitProgressFor(
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

			Expect(actions[0].GetEmitProgressAction()).To(Equal(dockerDownloadAction))

			fileDescriptorLimit := uint64(512)
			runAction := models.EmitProgressFor(
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

			Expect(actions[1].GetEmitProgressAction()).To(Equal(runAction))

			Expect(taskDef.MemoryMb).To(Equal(memoryMb))
			Expect(taskDef.DiskMb).To(Equal(diskMb))

			egressRules := []*models.SecurityGroupRule{
				{
					Protocol:     "TCP",
					Destinations: []string{"0.0.0.0/0"},
					PortRange:    &models.PortRange{Start: 80, End: 443},
				},
			}

			Expect(taskDef.EgressRules).To(Equal(egressRules))
		})

		It("uses the configured docker staging stack", func() {
			taskDef, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(taskDef.RootFs).To(Equal(models.PreloadedRootFS("penguin")))
		})

		It("gives the task a callback URL to call it back", func() {
			taskDef, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(taskDef.CompletionCallbackUrl).To(Equal(fmt.Sprintf("%s/v1/staging/%s/completed", "http://staging-url.com", "staging-guid")))
		})

		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				taskDef, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
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
				taskDef, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
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
				taskDef, _, _, err := docker.BuildRecipe("staging-guid", stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := taskDef.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(backend.DefaultStagingTimeout)))
			})
		})
	})

	Describe("BuildStagingResponse", func() {
		var (
			response          cc_messages.StagingResponseForCC
			failureReason     string
			buildError        error
			stagingResultJson []byte
			stagingResult     docker_app_lifecycle.StagingResult
		)

		BeforeEach(func() {
			stagingResult = docker_app_lifecycle.NewStagingResult(
				docker_app_lifecycle.ProcessTypes{"a": "b"},
				docker_app_lifecycle.LifecycleMetadata{
					DockerImage: "cloudfoundry/diego-docker-app",
				},
				"metadata",
			)
			var err error
			stagingResultJson, err = json.Marshal(stagingResult)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("with a successful task response", func() {
			BeforeEach(func() {
				taskResponse := &models.TaskCallbackResponse{
					Failed:        false,
					FailureReason: failureReason,
					Result:        string(stagingResultJson),
				}

				response, buildError = docker.BuildStagingResponse(taskResponse)
				Expect(buildError).NotTo(HaveOccurred())
			})

			It("populates a staging response correctly", func() {
				result := json.RawMessage(stagingResultJson)
				Expect(response).To(Equal(cc_messages.StagingResponseForCC{
					Result: &result,
				}))
			})

			Context("with a failed task response", func() {
				BeforeEach(func() {
					taskResponse := &models.TaskCallbackResponse{
						Failed:        true,
						FailureReason: "some-failure-reason",
						Result:        string(stagingResultJson),
					}

					response, buildError = docker.BuildStagingResponse(taskResponse)
					Expect(buildError).NotTo(HaveOccurred())
				})

				It("populates a staging response correctly", func() {
					Expect(response).To(Equal(cc_messages.StagingResponseForCC{
						Error: &cc_messages.StagingError{Message: "some-failure-reason was totally sanitized"},
					}))
				})
			})
		})
	})
})
