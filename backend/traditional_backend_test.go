package backend_test

import (
	"encoding/json"
	"fmt"
	"time"

	linux_circus "github.com/cloudfoundry-incubator/linux-circus"
	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/backend"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-golang/lager"
)

var _ = Describe("TraditionalBackend", func() {
	var (
		backend                        Backend
		stagingRequest                 cc_messages.StagingRequestFromCC
		stagingRequestJson             []byte
		config                         Config
		callbackURL                    string
		buildpackOrder                 string
		timeout                        int
		stack                          string
		memoryMB                       int
		diskMB                         int
		fileDescriptors                int
		buildArtifactsCacheDownloadUri string
		appId                          string
		taskId                         string
		buildpacks                     []cc_messages.Buildpack
		appBitsDownloadUri             string
		downloadTailorAction           models.Action
		downloadAppAction              models.Action
		downloadFirstBuildpackAction   models.Action
		downloadSecondBuildpackAction  models.Action
		downloadBuildArtifactsAction   models.Action
		runAction                      models.Action
		uploadDropletAction            models.Action
		uploadBuildArtifactsAction     models.Action
		egressRules                    []models.SecurityGroupRule
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
			Sanitizer: func(msg string) *cc_messages.StagingError {
				return &cc_messages.StagingError{Message: msg + " was totally sanitized"}
			},
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		backend = NewTraditionalBackend(config, logger)

		timeout = 900
		stack = "rabbit_hole"
		memoryMB = 2048
		diskMB = 3072
		fileDescriptors = 512
		buildArtifactsCacheDownloadUri = "http://example-uri.com/bunny-droppings"
		appId = "bunny"
		taskId = "hop"
		buildpacks = []cc_messages.Buildpack{
			{Name: "zfirst", Key: "zfirst-buildpack", Url: "first-buildpack-url"},
			{Name: "asecond", Key: "asecond-buildpack", Url: "second-buildpack-url"},
		}
		appBitsDownloadUri = "http://example-uri.com/bunny"

		downloadTailorAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/rabbit-hole-compiler",
				To:       "/tmp/circus",
				CacheKey: "tailor-rabbit_hole",
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
	})

	JustBeforeEach(func() {
		fileDescriptorLimit := uint64(fileDescriptors)
		runAction = models.EmitProgressFor(
			&models.RunAction{
				Path: "/tmp/circus/tailor",
				Args: []string{
					"-buildArtifactsCacheDir=/tmp/cache",
					"-buildDir=/tmp/app",
					"-buildpackOrder=" + buildpackOrder,
					"-buildpacksDir=/tmp/buildpacks",
					"-outputBuildArtifactsCache=/tmp/output-cache",
					"-outputDroplet=/tmp/droplet",
					"-outputMetadata=/tmp/result.json",
					"-skipCertVerify=false",
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

		var err error
		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:                          appId,
			TaskId:                         taskId,
			AppBitsDownloadUri:             appBitsDownloadUri,
			BuildArtifactsCacheDownloadUri: buildArtifactsCacheDownloadUri,
			BuildArtifactsCacheUploadUri:   "http://example-uri.com/bunny-uppings",
			DropletUploadUri:               "http://example-uri.com/droplet-upload",
			Stack:                          stack,
			FileDescriptors:                fileDescriptors,
			MemoryMB:                       memoryMB,
			DiskMB:                         diskMB,
			Buildpacks:                     buildpacks,
			Environment: cc_messages.Environment{
				{"VCAP_APPLICATION", "foo"},
				{"VCAP_SERVICES", "bar"},
			},
			EgressRules: egressRules,
			Timeout:     timeout,
		}

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

		Context("with a missing app bits download uri", func() {
			BeforeEach(func() {
				appBitsDownloadUri = ""
			})
			It("returns an error", func() {
				_, err := backend.BuildRecipe(stagingRequestJson)
				Ω(err).Should(Equal(ErrMissingAppBitsDownloadUri))
			})
		})
	})

	It("creates a cf-app-staging Task with staging instructions", func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(desiredTask.Domain).To(Equal("cf-app-staging"))
		Ω(desiredTask.TaskGuid).To(Equal("bunny-hop"))
		Ω(desiredTask.Stack).To(Equal("rabbit_hole"))
		Ω(desiredTask.LogGuid).To(Equal("bunny"))
		Ω(desiredTask.LogSource).To(Equal(TaskLogSource))
		Ω(desiredTask.ResultFile).To(Equal("/tmp/result.json"))
		Ω(desiredTask.Privileged).Should(BeTrue())

		var annotation models.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(models.StagingTaskAnnotation{
			AppId:  "bunny",
			TaskId: "hop",
		}))

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(Equal([]models.Action{
			downloadAppAction,
			models.EmitProgressFor(
				models.Parallel(
					downloadTailorAction,
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
		Ω(desiredTask.CPUWeight).To(Equal(StagingTaskCpuWeight))
		Ω(desiredTask.EgressRules).Should(ConsistOf(egressRules))
	})

	Context("with a custom buildpack", func() {
		var customBuildpack = "https://example.com/a/custom-buildpack.git"
		BeforeEach(func() {
			buildpacks = []cc_messages.Buildpack{
				{Name: cc_messages.CUSTOM_BUILDPACK, Key: customBuildpack, Url: customBuildpack},
			}
			buildpackOrder = customBuildpack
		})

		It("does not download any buildpacks", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(desiredTask.Domain).To(Equal("cf-app-staging"))
			Ω(desiredTask.TaskGuid).To(Equal("bunny-hop"))
			Ω(desiredTask.Stack).To(Equal("rabbit_hole"))
			Ω(desiredTask.LogGuid).To(Equal("bunny"))
			Ω(desiredTask.LogSource).To(Equal(TaskLogSource))
			Ω(desiredTask.ResultFile).To(Equal("/tmp/result.json"))

			var annotation models.StagingTaskAnnotation

			err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(annotation).Should(Equal(models.StagingTaskAnnotation{
				AppId:  "bunny",
				TaskId: "hop",
			}))

			actions := actionsFromDesiredTask(desiredTask)

			Ω(actions).Should(HaveLen(4))
			Ω(actions[0]).Should(Equal(downloadAppAction))
			Ω(actions[1]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadTailorAction,
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
			Ω(desiredTask.CPUWeight).To(Equal(StagingTaskCpuWeight))
		})
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

	Context("when build artifacts download uris are not provided", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = ""
		})

		It("does not instruct the executor to download the cache", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(actionsFromDesiredTask(desiredTask)).Should(Equal([]models.Action{
				downloadAppAction,
				models.EmitProgressFor(
					models.Parallel(
						downloadTailorAction,
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
			_, err := backend.BuildRecipe(stagingRequestJson)

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no compiler defined for requested stack"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL", func() {
		BeforeEach(func() {
			stack = "compiler_with_full_url"
		})

		It("uses the full URL in the download tailor action", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
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
			_, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).Should(HaveOccurred())
		})
	})

	Context("when build artifacts download url is not a valid url", func() {
		BeforeEach(func() {
			buildArtifactsCacheDownloadUri = "not-a-uri"
		})

		It("return a url parsing error", func() {
			_, err := backend.BuildRecipe(stagingRequestJson)

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(ContainSubstring("invalid URI"))
		})
	})

	Context("when skipping ssl certificate verification", func() {
		BeforeEach(func() {
			config.SkipCertVerify = true

			logger := lager.NewLogger("fakelogger")
			logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

			backend = NewTraditionalBackend(config, logger)
		})

		It("the tailor is told to skip certificate verification", func() {
			args := []string{
				"-buildArtifactsCacheDir=/tmp/cache",
				"-buildDir=/tmp/app",
				"-buildpackOrder=zfirst-buildpack,asecond-buildpack",
				"-buildpacksDir=/tmp/buildpacks",
				"-outputBuildArtifactsCache=/tmp/output-cache",
				"-outputDroplet=/tmp/droplet",
				"-outputMetadata=/tmp/result.json",
				"-skipCertVerify=true",
			}

			desiredTask, err := backend.BuildRecipe(stagingRequestJson)

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
		var buildError error
		var responseJson []byte

		Describe("BuildStagingResponseFromRequestError", func() {
			var requestJson []byte

			JustBeforeEach(func() {
				responseJson, buildError = backend.BuildStagingResponseFromRequestError(requestJson, "fake-error-message")
			})

			Context("with a valid request", func() {
				BeforeEach(func() {
					request := cc_messages.StagingRequestFromCC{
						AppId:  "myapp",
						TaskId: "mytask",
					}
					var err error
					requestJson, err = json.Marshal(request)
					Ω(err).ShouldNot(HaveOccurred())
				})

				It("returns a correctly populated staging response", func() {
					expectedResponse := cc_messages.StagingResponseForCC{
						AppId:  "myapp",
						TaskId: "mytask",
						Error:  &cc_messages.StagingError{Message: "fake-error-message was totally sanitized"},
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
							stagingResult := linux_circus.StagingResult{
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
							expectedResponse := cc_messages.StagingResponseForCC{
								AppId:                "app-id",
								TaskId:               "task-id",
								BuildpackKey:         "buildpack-key",
								DetectedBuildpack:    "detected-buildpack",
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
							expectedResponse := cc_messages.StagingResponseForCC{
								AppId:  "app-id",
								TaskId: "task-id",
								Error:  &cc_messages.StagingError{Message: "some-failure-reason was totally sanitized"},
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
