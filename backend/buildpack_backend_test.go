package backend_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cloudfoundry-incubator/buildpack_app_lifecycle"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/lager/lagertest"
)

var _ = Describe("TraditionalBackend", func() {
	var (
		traditional                    backend.Backend
		stagingRequest                 cc_messages.StagingRequestFromCC
		config                         backend.Config
		buildpackOrder                 string
		timeout                        int
		stack                          string
		memoryMB                       int
		diskMB                         int
		fileDescriptors                int
		buildArtifactsCacheDownloadUri string
		appId                          string
		stagingGuid                    string
		buildpacks                     []cc_messages.Buildpack
		appBitsDownloadUri             string
		downloadBuilderAction          models.Action
		downloadAppAction              models.Action
		downloadFirstBuildpackAction   models.Action
		downloadSecondBuildpackAction  models.Action
		downloadBuildArtifactsAction   models.Action
		runAction                      models.Action
		uploadDropletAction            models.Action
		uploadBuildArtifactsAction     models.Action
		egressRules                    []models.SecurityGroupRule
		environment                    cc_messages.Environment
	)

	BeforeEach(func() {
		stagerURL := "http://the-stager.example.com"

		config = backend.Config{
			TaskDomain:    "config-task-domain",
			StagerURL:     stagerURL,
			FileServerURL: "http://file-server.com",
			Lifecycles: map[string]string{
				"buildpack/penguin":                "penguin-compiler",
				"buildpack/rabbit_hole":            "rabbit-hole-compiler",
				"buildpack/compiler_with_full_url": "http://the-full-compiler-url",
				"buildpack/compiler_with_bad_url":  "ftp://the-bad-compiler-url",
			},
			Sanitizer: func(msg string) *cc_messages.StagingError {
				return &cc_messages.StagingError{Message: msg + " was totally sanitized"}
			},
		}

		logger := lagertest.NewTestLogger("test")

		traditional = backend.NewTraditionalBackend(config, logger)

		timeout = 900
		stack = "rabbit_hole"
		memoryMB = 2048
		diskMB = 3072
		fileDescriptors = 512
		buildArtifactsCacheDownloadUri = "http://example-uri.com/bunny-droppings"
		appId = "bunny"
		buildpacks = []cc_messages.Buildpack{
			{Name: "zfirst", Key: "zfirst-buildpack", Url: "first-buildpack-url"},
			{Name: "asecond", Key: "asecond-buildpack", Url: "second-buildpack-url"},
		}
		appBitsDownloadUri = "http://example-uri.com/bunny"

		downloadBuilderAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/rabbit-hole-compiler",
				To:       "/tmp/lifecycle",
				CacheKey: "builder-rabbit_hole",
			},
			"",
			"",
			"Failed to set up staging environment",
		)

		downloadAppAction = &models.DownloadAction{
			Artifact: "app package",
			From:     "http://example-uri.com/bunny",
			To:       "/tmp/app",
		}

		downloadFirstBuildpackAction = &models.DownloadAction{
			Artifact: "zfirst",
			From:     "first-buildpack-url",
			To:       "/tmp/buildpacks/0fe7d5fc3f73b0ab8682a664da513fbd",
			CacheKey: "zfirst-buildpack",
		}

		downloadSecondBuildpackAction = &models.DownloadAction{
			Artifact: "asecond",
			From:     "second-buildpack-url",
			To:       "/tmp/buildpacks/58015c32d26f0ad3418f87dd9bf47797",
			CacheKey: "asecond-buildpack",
		}

		downloadBuildArtifactsAction = models.Try(
			&models.DownloadAction{
				Artifact: "build artifacts cache",
				From:     "http://example-uri.com/bunny-droppings",
				To:       "/tmp/cache",
			},
		)

		buildpackOrder = "zfirst-buildpack,asecond-buildpack"

		uploadDropletAction = &models.UploadAction{
			Artifact: "droplet",
			From:     "/tmp/droplet",
			To:       "http://file-server.com/v1/droplet/bunny?" + models.CcDropletUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fdroplet-upload" + "&" + models.CcTimeoutKey + "=" + fmt.Sprintf("%d", timeout),
		}

		uploadBuildArtifactsAction = models.Try(
			&models.UploadAction{
				Artifact: "build artifacts cache",
				From:     "/tmp/output-cache",
				To:       "http://file-server.com/v1/build_artifacts/bunny?" + models.CcBuildArtifactsUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fbunny-uppings" + "&" + models.CcTimeoutKey + "=" + fmt.Sprintf("%d", timeout),
			},
		)

		egressRules = []models.SecurityGroupRule{
			{
				Protocol:     "TCP",
				Destinations: []string{"0.0.0.0/0"},
				PortRange:    &models.PortRange{Start: 80, End: 443},
			},
		}

		environment = cc_messages.Environment{
			{"VCAP_APPLICATION", "foo"},
			{"VCAP_SERVICES", "bar"},
		}
	})

	JustBeforeEach(func() {
		fileDescriptorLimit := uint64(fileDescriptors)
		runAction = models.EmitProgressFor(
			&models.RunAction{
				Path: "/tmp/lifecycle/builder",
				Args: []string{
					"-buildArtifactsCacheDir=/tmp/cache",
					"-buildDir=/tmp/app",
					"-buildpackOrder=" + buildpackOrder,
					"-buildpacksDir=/tmp/buildpacks",
					"-outputBuildArtifactsCache=/tmp/output-cache",
					"-outputDroplet=/tmp/droplet",
					"-outputMetadata=/tmp/result.json",
					"-skipCertVerify=false",
					"-skipDetect=" + strconv.FormatBool(buildpacks[0].SkipDetect),
				},
				Env: []models.EnvironmentVariable{
					{"VCAP_APPLICATION", "foo"},
					{"VCAP_SERVICES", "bar"},
				},
				ResourceLimits: models.ResourceLimits{Nofile: &fileDescriptorLimit},
			},
			"Staging...",
			"Staging complete",
			"Staging failed",
		)

		buildpackStagingData := cc_messages.BuildpackStagingData{
			AppBitsDownloadUri:             appBitsDownloadUri,
			BuildArtifactsCacheDownloadUri: buildArtifactsCacheDownloadUri,
			BuildArtifactsCacheUploadUri:   "http://example-uri.com/bunny-uppings",
			Buildpacks:                     buildpacks,
			DropletUploadUri:               "http://example-uri.com/droplet-upload",
			Stack:                          stack,
		}
		lifecycleDataJSON, err := json.Marshal(buildpackStagingData)
		Ω(err).ShouldNot(HaveOccurred())

		lifecycleData := json.RawMessage(lifecycleDataJSON)

		stagingGuid = "a-staging-guid"

		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:           appId,
			LogGuid:         appId,
			FileDescriptors: fileDescriptors,
			MemoryMB:        memoryMB,
			DiskMB:          diskMB,
			Environment:     environment,
			EgressRules:     egressRules,
			Timeout:         timeout,
			Lifecycle:       "buildpack",
			LifecycleData:   &lifecycleData,
		}
	})

	Describe("request validation", func() {
		Context("with a missing app bits download uri", func() {
			BeforeEach(func() {
				appBitsDownloadUri = ""
			})

			It("returns an error", func() {
				_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).Should(Equal(backend.ErrMissingAppBitsDownloadUri))
			})
		})

		Context("with missing lifecycle data", func() {
			JustBeforeEach(func() {
				stagingRequest.LifecycleData = nil
			})

			It("returns an error", func() {
				_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).Should(Equal(backend.ErrMissingLifecycleData))
			})
		})
	})

	It("creates a cf-app-staging Task with staging instructions", func() {
		desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.Domain).To(Equal("config-task-domain"))
		Ω(desiredTask.TaskGuid).To(Equal(stagingGuid))
		Ω(desiredTask.RootFS).To(Equal(models.PreloadedRootFS("rabbit_hole")))
		Ω(desiredTask.LogGuid).To(Equal("bunny"))
		Ω(desiredTask.MetricsGuid).Should(BeEmpty()) // do not emit metrics for staging!
		Ω(desiredTask.LogSource).To(Equal(backend.TaskLogSource))
		Ω(desiredTask.ResultFile).To(Equal("/tmp/result.json"))
		Ω(desiredTask.Privileged).Should(BeTrue())

		var annotation cc_messages.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(cc_messages.StagingTaskAnnotation{
			Lifecycle: "buildpack",
		}))

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(Equal([]models.Action{
			downloadAppAction,
			models.EmitProgressFor(
				models.Parallel(
					downloadBuilderAction,
					downloadFirstBuildpackAction,
					downloadSecondBuildpackAction,
					downloadBuildArtifactsAction,
				),
				"No buildpack specified; fetching standard buildpacks to detect and build your application.\n"+
					"Downloading buildpacks (zfirst, asecond), build artifacts cache...",
				"Downloaded buildpacks",
				"Downloading buildpacks failed",
			),
			runAction,
			models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, build artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			),
		}))

		Ω(desiredTask.MemoryMB).To(Equal(memoryMB))
		Ω(desiredTask.DiskMB).To(Equal(diskMB))
		Ω(desiredTask.CPUWeight).To(Equal(backend.StagingTaskCpuWeight))
		Ω(desiredTask.EgressRules).Should(ConsistOf(egressRules))
	})

	Context("with a speicifed buildpack", func() {
		BeforeEach(func() {
			buildpacks = buildpacks[:1]
			buildpacks[0].SkipDetect = true
			buildpackOrder = "zfirst-buildpack"
		})

		It("it downloads the buildpack and skips detect", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			actions := actionsFromDesiredTask(desiredTask)

			Ω(actions).Should(HaveLen(4))
			Ω(actions[0]).Should(Equal(downloadAppAction))
			Ω(actions[1]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadBuilderAction,
					downloadFirstBuildpackAction,
					downloadBuildArtifactsAction,
				),
				"Downloading buildpacks (zfirst), build artifacts cache...",
				"Downloaded buildpacks",
				"Downloading buildpacks failed",
			)))
			Ω(actions[2]).Should(Equal(runAction))
			Ω(actions[3]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, build artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			)))
		})
	})

	Context("with a custom buildpack", func() {
		var customBuildpack = "https://example.com/a/custom-buildpack.git"
		BeforeEach(func() {
			buildpacks = []cc_messages.Buildpack{
				{Name: "custom", Key: customBuildpack, Url: customBuildpack, SkipDetect: true},
			}
			buildpackOrder = customBuildpack
		})

		It("does not download any buildpacks and skips detect", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(desiredTask.Domain).To(Equal("config-task-domain"))
			Ω(desiredTask.TaskGuid).To(Equal(stagingGuid))
			Ω(desiredTask.RootFS).To(Equal(models.PreloadedRootFS("rabbit_hole")))
			Ω(desiredTask.LogGuid).To(Equal("bunny"))
			Ω(desiredTask.LogSource).To(Equal(backend.TaskLogSource))
			Ω(desiredTask.ResultFile).To(Equal("/tmp/result.json"))

			var annotation cc_messages.StagingTaskAnnotation

			err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(annotation).Should(Equal(cc_messages.StagingTaskAnnotation{
				Lifecycle: "buildpack",
			}))

			actions := actionsFromDesiredTask(desiredTask)

			Ω(actions).Should(HaveLen(4))
			Ω(actions[0]).Should(Equal(downloadAppAction))
			Ω(actions[1]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadBuilderAction,
					downloadBuildArtifactsAction,
				),
				"Downloading buildpacks ("+customBuildpack+"), build artifacts cache...",
				"Downloaded buildpacks",
				"Downloading buildpacks failed",
			)))
			Ω(actions[2]).Should(Equal(runAction))
			Ω(actions[3]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, build artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			)))

			Ω(desiredTask.MemoryMB).To(Equal(memoryMB))
			Ω(desiredTask.DiskMB).To(Equal(diskMB))
			Ω(desiredTask.CPUWeight).To(Equal(backend.StagingTaskCpuWeight))
		})
	})

	It("gives the task a callback URL to call it back", func() {
		desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(desiredTask.CompletionCallbackURL).Should(Equal(fmt.Sprintf("%s/v1/staging/%s/completed", config.StagerURL, stagingGuid)))
	})

	Describe("staging action timeout", func() {
		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
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
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
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
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())

				timeoutAction := desiredTask.Action
				Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
				Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(backend.DefaultStagingTimeout))
			})
		})
	})

	Context("when build artifacts download uris are not provided", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = ""
		})

		It("does not instruct the executor to download the cache", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(actionsFromDesiredTask(desiredTask)).Should(Equal([]models.Action{
				downloadAppAction,
				models.EmitProgressFor(
					models.Parallel(
						downloadBuilderAction,
						downloadFirstBuildpackAction,
						downloadSecondBuildpackAction,
					),
					"No buildpack specified; fetching standard buildpacks to detect and build your application.\n"+
						"Downloading buildpacks (zfirst, asecond)...",
					"Downloaded buildpacks",
					"Downloading buildpacks failed",
				),
				runAction,
				models.EmitProgressFor(
					models.Parallel(
						uploadDropletAction,
						uploadBuildArtifactsAction,
					),
					"Uploading droplet, build artifacts cache...",
					"Uploading complete",
					"Uploading failed",
				),
			}))
		})
	})

	Context("when no compiler is defined for the requested stack in backend configuration", func() {
		BeforeEach(func() {
			stack = "no_such_stack"
		})

		It("returns an error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no compiler defined for requested stack"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL", func() {
		BeforeEach(func() {
			stack = "compiler_with_full_url"
		})

		It("uses the full URL in the download builder action", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			actions := actionsFromDesiredTask(desiredTask)
			downloadAction := actions[1].(*models.EmitProgressAction).Action.(*models.ParallelAction).Actions[0].(*models.EmitProgressAction).Action.(*models.DownloadAction)
			Ω(downloadAction.From).Should(Equal("http://the-full-compiler-url"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL with an unexpected scheme", func() {
		BeforeEach(func() {
			stack = "compiler_with_bad_url"
		})

		It("returns an error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Ω(err).Should(HaveOccurred())
		})
	})

	Context("when build artifacts download url is not a valid url", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = "not-a-uri"
		})

		It("return a url parsing error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(ContainSubstring("invalid URI"))
		})
	})

	Context("when skipping ssl certificate verification", func() {
		BeforeEach(func() {
			config.SkipCertVerify = true

			logger := lager.NewLogger("fakelogger")
			logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

			traditional = backend.NewTraditionalBackend(config, logger)
		})

		It("the builder is told to skip certificate verification", func() {
			args := []string{
				"-buildArtifactsCacheDir=/tmp/cache",
				"-buildDir=/tmp/app",
				"-buildpackOrder=zfirst-buildpack,asecond-buildpack",
				"-buildpacksDir=/tmp/buildpacks",
				"-outputBuildArtifactsCache=/tmp/output-cache",
				"-outputDroplet=/tmp/droplet",
				"-outputMetadata=/tmp/result.json",
				"-skipCertVerify=true",
				"-skipDetect=false",
			}

			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)

			Ω(err).ShouldNot(HaveOccurred())

			timeoutAction := desiredTask.Action
			Ω(timeoutAction).Should(BeAssignableToTypeOf(&models.TimeoutAction{}))
			Ω(timeoutAction.(*models.TimeoutAction).Timeout).Should(Equal(15 * time.Minute))

			serialAction := timeoutAction.(*models.TimeoutAction).Action
			Ω(serialAction).Should(BeAssignableToTypeOf(&models.SerialAction{}))

			emitProgressAction := serialAction.(*models.SerialAction).Actions[2]
			Ω(emitProgressAction).Should(BeAssignableToTypeOf(&models.EmitProgressAction{}))

			runAction := emitProgressAction.(*models.EmitProgressAction).Action
			Ω(runAction).Should(BeAssignableToTypeOf(&models.RunAction{}))
			Ω(runAction.(*models.RunAction).Args).Should(Equal(args))
		})
	})

	Describe("response building", func() {
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
				response, buildError = traditional.BuildStagingResponse(taskResponse)
			})

			Context("with a valid annotation", func() {
				BeforeEach(func() {
					annotation := cc_messages.StagingTaskAnnotation{
						Lifecycle: "buildpack",
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
							stagingResult := buildpack_app_lifecycle.StagingResult{
								BuildpackKey:         "buildpack-key",
								DetectedBuildpack:    "detected-buildpack",
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
							}
							var err error
							stagingResultJson, err = json.Marshal(stagingResult)
							Ω(err).ShouldNot(HaveOccurred())
						})

						It("populates a staging response correctly", func() {
							expectedBuildpackResponse := cc_messages.BuildpackStagingResponse{
								BuildpackKey:      "buildpack-key",
								DetectedBuildpack: "detected-buildpack",
							}
							lifecycleDataJSON, err := json.Marshal(expectedBuildpackResponse)
							Ω(err).ShouldNot(HaveOccurred())

							responseLifecycleData := json.RawMessage(lifecycleDataJSON)

							Ω(buildError).ShouldNot(HaveOccurred())
							Ω(response).Should(Equal(cc_messages.StagingResponseForCC{
								ExecutionMetadata:    "metadata",
								DetectedStartCommand: map[string]string{"a": "b"},
								LifecycleData:        &responseLifecycleData,
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
