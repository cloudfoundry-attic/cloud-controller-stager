package backend_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cloudfoundry-incubator/bbs/models"
	"github.com/cloudfoundry-incubator/buildpack_app_lifecycle"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
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
		downloadBuilderAction          models.ActionInterface
		downloadAppAction              models.ActionInterface
		downloadFirstBuildpackAction   models.ActionInterface
		downloadSecondBuildpackAction  models.ActionInterface
		downloadBuildArtifactsAction   models.ActionInterface
		runAction                      models.ActionInterface
		uploadDropletAction            models.ActionInterface
		uploadBuildArtifactsAction     models.ActionInterface
		egressRules                    []*models.SecurityGroupRule
		environment                    []*models.EnvironmentVariable
	)

	BeforeEach(func() {
		stagerURL := "http://the-stager.example.com"

		config = backend.Config{
			TaskDomain:    "config-task-domain",
			StagerURL:     stagerURL,
			FileServerURL: "http://file-server.com",
			CCUploaderURL: "http://cc-uploader.com",
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
				CacheKey: "buildpack-rabbit_hole-lifecycle",
				User:     "vcap",
			},
			"",
			"",
			"Failed to set up staging environment",
		)

		downloadAppAction = &models.DownloadAction{
			Artifact: "app package",
			From:     "http://example-uri.com/bunny",
			To:       "/tmp/app",
			User:     "vcap",
		}

		downloadFirstBuildpackAction = &models.DownloadAction{
			Artifact: "zfirst",
			From:     "first-buildpack-url",
			To:       "/tmp/buildpacks/0fe7d5fc3f73b0ab8682a664da513fbd",
			CacheKey: "zfirst-buildpack",
			User:     "vcap",
		}

		downloadSecondBuildpackAction = &models.DownloadAction{
			Artifact: "asecond",
			From:     "second-buildpack-url",
			To:       "/tmp/buildpacks/58015c32d26f0ad3418f87dd9bf47797",
			CacheKey: "asecond-buildpack",
			User:     "vcap",
		}

		downloadBuildArtifactsAction = models.Try(
			&models.DownloadAction{
				Artifact: "build artifacts cache",
				From:     "http://example-uri.com/bunny-droppings",
				To:       "/tmp/cache",
				User:     "vcap",
			},
		)

		buildpackOrder = "zfirst-buildpack,asecond-buildpack"

		uploadDropletAction = &models.UploadAction{
			Artifact: "droplet",
			From:     "/tmp/droplet",
			To:       "http://cc-uploader.com/v1/droplet/bunny?" + cc_messages.CcDropletUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fdroplet-upload" + "&" + cc_messages.CcTimeoutKey + "=" + fmt.Sprintf("%d", timeout),
			User:     "vcap",
		}

		uploadBuildArtifactsAction = models.Try(
			&models.UploadAction{
				Artifact: "build artifacts cache",
				From:     "/tmp/output-cache",
				To:       "http://cc-uploader.com/v1/build_artifacts/bunny?" + cc_messages.CcBuildArtifactsUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fbunny-uppings" + "&" + cc_messages.CcTimeoutKey + "=" + fmt.Sprintf("%d", timeout),
				User:     "vcap",
			},
		)

		egressRules = []*models.SecurityGroupRule{
			{
				Protocol:     "TCP",
				Destinations: []string{"0.0.0.0/0"},
				PortRange:    &models.PortRange{Start: 80, End: 443},
			},
		}

		environment = []*models.EnvironmentVariable{
			{"VCAP_APPLICATION", "foo"},
			{"VCAP_SERVICES", "bar"},
		}
	})

	JustBeforeEach(func() {
		fileDescriptorLimit := uint64(fileDescriptors)
		runAction = models.EmitProgressFor(
			&models.RunAction{
				User: "vcap",
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
				Env: []*models.EnvironmentVariable{
					{"VCAP_APPLICATION", "foo"},
					{"VCAP_SERVICES", "bar"},
				},
				ResourceLimits: &models.ResourceLimits{Nofile: &fileDescriptorLimit},
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
		Expect(err).NotTo(HaveOccurred())

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
				Expect(err).To(Equal(backend.ErrMissingAppBitsDownloadUri))
			})
		})

		Context("with missing lifecycle data", func() {
			JustBeforeEach(func() {
				stagingRequest.LifecycleData = nil
			})

			It("returns an error", func() {
				_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).To(Equal(backend.ErrMissingLifecycleData))
			})
		})
	})

	It("creates a cf-app-staging Task with staging instructions", func() {
		desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
		Expect(err).NotTo(HaveOccurred())

		Expect(desiredTask.Domain).To(Equal("config-task-domain"))
		Expect(desiredTask.TaskGuid).To(Equal(stagingGuid))
		Expect(desiredTask.RootFS).To(Equal(models.PreloadedRootFS("rabbit_hole")))
		Expect(desiredTask.LogGuid).To(Equal("bunny"))
		Expect(desiredTask.MetricsGuid).To(BeEmpty()) // do not emit metrics for staging!
		Expect(desiredTask.LogSource).To(Equal(backend.TaskLogSource))
		Expect(desiredTask.ResultFile).To(Equal("/tmp/result.json"))
		Expect(desiredTask.Privileged).To(BeTrue())

		var annotation cc_messages.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Expect(err).NotTo(HaveOccurred())

		Expect(annotation).To(Equal(cc_messages.StagingTaskAnnotation{
			Lifecycle: "buildpack",
		}))

		actions := actionsFromDesiredTask(desiredTask)
		Expect(actions).To(Equal(models.Serial(
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
		).Actions))

		Expect(desiredTask.MemoryMB).To(Equal(memoryMB))
		Expect(desiredTask.DiskMB).To(Equal(diskMB))
		Expect(desiredTask.CPUWeight).To(Equal(backend.StagingTaskCpuWeight))
		Expect(desiredTask.EgressRules).To(ConsistOf(egressRules))
	})

	Context("with a specified buildpack", func() {
		BeforeEach(func() {
			buildpacks = buildpacks[:1]
			buildpacks[0].SkipDetect = true
			buildpackOrder = "zfirst-buildpack"
		})

		It("it downloads the buildpack and skips detect", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			actions := actionsFromDesiredTask(desiredTask)

			Expect(actions).To(HaveLen(4))
			Expect(actions[0].GetDownloadAction()).To(Equal(downloadAppAction))
			Expect(actions[1].GetEmitProgressAction()).To(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadBuilderAction,
					downloadFirstBuildpackAction,
					downloadBuildArtifactsAction,
				),
				"Downloading buildpacks (zfirst), build artifacts cache...",
				"Downloaded buildpacks",
				"Downloading buildpacks failed",
			)))

			Expect(actions[2].GetEmitProgressAction()).To(Equal(runAction))
			Expect(actions[3].GetEmitProgressAction()).To(Equal(models.EmitProgressFor(
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
			Expect(err).NotTo(HaveOccurred())

			Expect(desiredTask.Domain).To(Equal("config-task-domain"))
			Expect(desiredTask.TaskGuid).To(Equal(stagingGuid))
			Expect(desiredTask.RootFS).To(Equal(models.PreloadedRootFS("rabbit_hole")))
			Expect(desiredTask.LogGuid).To(Equal("bunny"))
			Expect(desiredTask.LogSource).To(Equal(backend.TaskLogSource))
			Expect(desiredTask.ResultFile).To(Equal("/tmp/result.json"))

			var annotation cc_messages.StagingTaskAnnotation

			err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
			Expect(err).NotTo(HaveOccurred())

			Expect(annotation).To(Equal(cc_messages.StagingTaskAnnotation{
				Lifecycle: "buildpack",
			}))

			actions := actionsFromDesiredTask(desiredTask)

			Expect(actions).To(HaveLen(4))
			Expect(actions[0].GetDownloadAction()).To(Equal(downloadAppAction))
			Expect(actions[1].GetEmitProgressAction()).To(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadBuilderAction,
					downloadBuildArtifactsAction,
				),
				"Downloading buildpacks ("+customBuildpack+"), build artifacts cache...",
				"Downloaded buildpacks",
				"Downloading buildpacks failed",
			)))

			Expect(actions[2].GetEmitProgressAction()).To(Equal(runAction))
			Expect(actions[3].GetEmitProgressAction()).To(Equal(models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, build artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			)))

			Expect(desiredTask.MemoryMB).To(Equal(memoryMB))
			Expect(desiredTask.DiskMB).To(Equal(diskMB))
			Expect(desiredTask.CPUWeight).To(Equal(backend.StagingTaskCpuWeight))
		})
	})

	It("gives the task a callback URL to call it back", func() {
		desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(desiredTask.CompletionCallbackURL).To(Equal(fmt.Sprintf("%s/v1/staging/%s/completed", config.StagerURL, stagingGuid)))
	})

	Describe("staging action timeout", func() {
		Context("when a positive timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 5
			})

			It("passes the timeout along", func() {
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := desiredTask.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(time.Duration(timeout) * time.Second)))
			})
		})

		Context("when a 0 timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = 0
			})

			It("uses the default timeout", func() {
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := desiredTask.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(backend.DefaultStagingTimeout)))
			})
		})

		Context("when a negative timeout is specified in the staging request from CC", func() {
			BeforeEach(func() {
				timeout = -3
			})

			It("uses the default timeout", func() {
				desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
				Expect(err).NotTo(HaveOccurred())

				timeoutAction := desiredTask.Action.GetTimeoutAction()
				Expect(timeoutAction).NotTo(BeNil())
				Expect(timeoutAction.Timeout).To(Equal(int64(backend.DefaultStagingTimeout)))
			})
		})
	})

	Context("when build artifacts download uris are not provided", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = ""
		})

		It("does not instruct the executor to download the cache", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			Expect(actionsFromDesiredTask(desiredTask)).To(Equal(models.Serial(
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
			).Actions))
		})
	})

	Context("when no compiler is defined for the requested stack in backend configuration", func() {
		BeforeEach(func() {
			stack = "no_such_stack"
		})

		It("returns an error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("no compiler defined for requested stack"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL", func() {
		BeforeEach(func() {
			stack = "compiler_with_full_url"
		})

		It("uses the full URL in the download builder action", func() {
			desiredTask, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Expect(err).NotTo(HaveOccurred())

			actions := actionsFromDesiredTask(desiredTask)
			downloadAction := actions[1].GetEmitProgressAction().Action.GetParallelAction().Actions[0].GetEmitProgressAction().Action.GetDownloadAction()
			Expect(downloadAction.From).To(Equal("http://the-full-compiler-url"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL with an unexpected scheme", func() {
		BeforeEach(func() {
			stack = "compiler_with_bad_url"
		})

		It("returns an error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("when build artifacts download url is not a valid url", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = "not-a-uri"
		})

		It("return a url parsing error", func() {
			_, err := traditional.BuildRecipe(stagingGuid, stagingRequest)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid URI"))
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

			Expect(err).NotTo(HaveOccurred())

			timeoutAction := desiredTask.Action.GetTimeoutAction()
			Expect(timeoutAction).NotTo(BeNil())
			Expect(timeoutAction.Timeout).To(Equal(int64(15 * time.Minute)))

			serialAction := timeoutAction.Action.GetSerialAction()
			Expect(serialAction).NotTo(BeNil())

			emitProgressAction := serialAction.Actions[2].GetEmitProgressAction()
			Expect(emitProgressAction).NotTo(BeNil())

			runAction := emitProgressAction.Action.GetRunAction()
			Expect(runAction).NotTo(BeNil())
			Expect(runAction.Args).To(Equal(args))
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
					Expect(err).NotTo(HaveOccurred())
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
							Expect(err).NotTo(HaveOccurred())
						})

						It("populates a staging response correctly", func() {
							expectedBuildpackResponse := cc_messages.BuildpackStagingResponse{
								BuildpackKey:      "buildpack-key",
								DetectedBuildpack: "detected-buildpack",
							}
							lifecycleDataJSON, err := json.Marshal(expectedBuildpackResponse)
							Expect(err).NotTo(HaveOccurred())

							responseLifecycleData := json.RawMessage(lifecycleDataJSON)

							Expect(buildError).NotTo(HaveOccurred())
							Expect(response).To(Equal(cc_messages.StagingResponseForCC{
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
