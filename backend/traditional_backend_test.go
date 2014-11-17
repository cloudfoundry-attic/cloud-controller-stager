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

var _ = Describe("TraditionalBackend", func() {
	var (
		backend                       Backend
		stagingRequest                cc_messages.StagingRequestFromCC
		stagingRequestJson            []byte
		downloadTailorAction          models.ExecutorAction
		downloadAppAction             models.ExecutorAction
		downloadFirstBuildpackAction  models.ExecutorAction
		downloadSecondBuildpackAction models.ExecutorAction
		downloadBuildArtifactsAction  models.ExecutorAction
		runAction                     models.ExecutorAction
		uploadDropletAction           models.ExecutorAction
		uploadBuildArtifactsAction    models.ExecutorAction
		config                        Config
		callbackURL                   string
		buildpackOrder                string
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

		backend = NewTraditionalBackend(config, logger)

		stagingRequest = cc_messages.StagingRequestFromCC{
			AppId:                          "bunny",
			TaskId:                         "hop",
			AppBitsDownloadUri:             "http://example-uri.com/bunny",
			BuildArtifactsCacheDownloadUri: "http://example-uri.com/bunny-droppings",
			BuildArtifactsCacheUploadUri:   "http://example-uri.com/bunny-uppings",
			DropletUploadUri:               "http://example-uri.com/droplet-upload",
			Stack:                          "rabbit_hole",
			FileDescriptors:                512,
			MemoryMB:                       2048,
			DiskMB:                         3072,
			Buildpacks: []cc_messages.Buildpack{
				{Name: "zfirst", Key: "zfirst-buildpack", Url: "first-buildpack-url"},
				{Name: "asecond", Key: "asecond-buildpack", Url: "second-buildpack-url"},
			},
			Environment: cc_messages.Environment{
				{"VCAP_APPLICATION", "foo"},
				{"VCAP_SERVICES", "bar"},
			},
		}

		downloadTailorAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     "http://file-server.com/v1/static/rabbit-hole-compiler",
					To:       "/tmp/circus",
					CacheKey: "tailor-rabbit_hole",
				},
			},
			"",
			"",
			"Failed to Download Tailor",
		)

		downloadAppAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From: "http://example-uri.com/bunny",
					To:   "/app",
				},
			},
			"",
			"Downloaded App Package",
			"Failed to Download App Package",
		)

		downloadFirstBuildpackAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     "first-buildpack-url",
					To:       "/tmp/buildpacks/0fe7d5fc3f73b0ab8682a664da513fbd",
					CacheKey: "zfirst-buildpack",
				},
			},
			"",
			"Downloaded Buildpack: zfirst",
			"Failed to Download Buildpack: zfirst",
		)

		downloadSecondBuildpackAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.DownloadAction{
					From:     "second-buildpack-url",
					To:       "/tmp/buildpacks/58015c32d26f0ad3418f87dd9bf47797",
					CacheKey: "asecond-buildpack",
				},
			},
			"",
			"Downloaded Buildpack: asecond",
			"Failed to Download Buildpack: asecond",
		)

		downloadBuildArtifactsAction = models.Try(
			models.EmitProgressFor(
				models.ExecutorAction{
					models.DownloadAction{
						From: "http://example-uri.com/bunny-droppings",
						To:   "/tmp/cache",
					},
				},
				"",
				"Downloaded Build Artifacts Cache",
				"No Build Artifacts Cache Found.  Proceeding...",
			),
		)

		buildpackOrder = "zfirst-buildpack,asecond-buildpack"

		uploadDropletAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.UploadAction{
					From: "/tmp/droplet",
					To:   "http://file-server.com/v1/droplet/bunny?" + models.CcDropletUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fdroplet-upload",
				},
			},
			"",
			"Droplet Uploaded",
			"Failed to Upload Droplet",
		)

		uploadBuildArtifactsAction = models.Try(
			models.EmitProgressFor(
				models.ExecutorAction{
					models.UploadAction{
						From: "/tmp/output-cache",
						To:   "http://file-server.com/v1/build_artifacts/bunny?" + models.CcBuildArtifactsUploadUriKey + "=http%3A%2F%2Fexample-uri.com%2Fbunny-uppings",
					},
				},
				"",
				"Uploaded Build Artifacts Cache",
				"Failed to Upload Build Artifacts Cache.  Proceeding...",
			),
		)
	})

	JustBeforeEach(func() {
		fileDescriptorLimit := uint64(512)
		runAction = models.EmitProgressFor(
			models.ExecutorAction{
				models.RunAction{
					Path: "/tmp/circus/tailor",
					Args: []string{
						"-appDir=/app",
						"-buildArtifactsCacheDir=/tmp/cache",
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
					Timeout:        15 * time.Minute,
					ResourceLimits: models.ResourceLimits{Nofile: &fileDescriptorLimit},
				},
			},
			"Staging...",
			"Staging Complete",
			"Staging Failed",
		)

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

		Context("with a missing app bits download uri", func() {
			BeforeEach(func() {
				stagingRequest.AppBitsDownloadUri = ""
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

		var annotation models.StagingTaskAnnotation

		err = json.Unmarshal([]byte(desiredTask.Annotation), &annotation)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(annotation).Should(Equal(models.StagingTaskAnnotation{
			AppId:  "bunny",
			TaskId: "hop",
		}))

		Ω(actionsFromExecutorSerialAction(desiredTask.Action)).Should(Equal([]models.ExecutorAction{
			models.EmitProgressFor(
				models.Parallel(
					downloadTailorAction,
					downloadAppAction,
					downloadFirstBuildpackAction,
					downloadSecondBuildpackAction,
					downloadBuildArtifactsAction,
				),
				"Fetching app, buildpacks (zfirst, asecond), artifacts cache...",
				"Fetching complete",
				"Fetching failed",
			),
			runAction,
			models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			),
		}))

		Ω(desiredTask.MemoryMB).To(Equal(2048))
		Ω(desiredTask.DiskMB).To(Equal(3072))
		Ω(desiredTask.CPUWeight).To(Equal(StagingTaskCpuWeight))
	})

	Context("with a custom buildpack", func() {
		var customBuildpack = "https://example.com/a/custom-buildpack.git"
		BeforeEach(func() {
			stagingRequest.Buildpacks = []cc_messages.Buildpack{
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

			actions := actionsFromExecutorSerialAction(desiredTask.Action)

			Ω(actions).Should(HaveLen(3))
			Ω(actions[0]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					downloadTailorAction,
					downloadAppAction,
					downloadBuildArtifactsAction,
				),
				"Fetching app, buildpacks ("+customBuildpack+"), artifacts cache...",
				"Fetching complete",
				"Fetching failed",
			)))
			Ω(actions[1]).Should(Equal(runAction))
			Ω(actions[2]).Should(Equal(models.EmitProgressFor(
				models.Parallel(
					uploadDropletAction,
					uploadBuildArtifactsAction,
				),
				"Uploading droplet, artifacts cache...",
				"Uploading complete",
				"Uploading failed",
			)))

			Ω(desiredTask.MemoryMB).To(Equal(2048))
			Ω(desiredTask.DiskMB).To(Equal(3072))
			Ω(desiredTask.CPUWeight).To(Equal(StagingTaskCpuWeight))
		})
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

				runAction = models.EmitProgressFor(
					models.ExecutorAction{
						models.RunAction{
							Path: "/tmp/circus/tailor",
							Args: []string{
								"-appDir=/app",
								"-buildArtifactsCacheDir=/tmp/cache",
								"-buildpackOrder=zfirst-buildpack,asecond-buildpack",
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
							Timeout:        15 * time.Minute,
							ResourceLimits: models.ResourceLimits{Nofile: &config.MinFileDescriptors},
						},
					},
					"Staging...",
					"Staging Complete",
					"Staging Failed",
				)

				Ω(desiredTask.Action.Action.(models.SerialAction).Actions).Should(Equal([]models.ExecutorAction{
					models.EmitProgressFor(
						models.Parallel(
							downloadTailorAction,
							downloadAppAction,
							downloadFirstBuildpackAction,
							downloadSecondBuildpackAction,
							downloadBuildArtifactsAction,
						),
						"Fetching app, buildpacks (zfirst, asecond), artifacts cache...",
						"Fetching complete",
						"Fetching failed",
					),
					runAction,
					models.EmitProgressFor(
						models.Parallel(
							uploadDropletAction,
							uploadBuildArtifactsAction,
						),
						"Uploading droplet, artifacts cache...",
						"Uploading complete",
						"Uploading failed",
					),
				}))
			})
		})
	})

	Context("when build artifacts download uris are not provided", func() {
		BeforeEach(func() {
			stagingRequest.BuildArtifactsCacheDownloadUri = ""
		})

		It("does not instruct the executor to download the cache", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(desiredTask.Action.Action.(models.SerialAction).Actions).Should(Equal([]models.ExecutorAction{
				models.EmitProgressFor(
					models.Parallel(
						downloadTailorAction,
						downloadAppAction,
						downloadFirstBuildpackAction,
						downloadSecondBuildpackAction,
					),
					"Fetching app, buildpacks (zfirst, asecond)...",
					"Fetching complete",
					"Fetching failed",
				),
				runAction,
				models.EmitProgressFor(
					models.Parallel(
						uploadDropletAction,
						uploadBuildArtifactsAction,
					),
					"Uploading droplet, artifacts cache...",
					"Uploading complete",
					"Uploading failed",
				),
			}))
		})
	})

	Context("when no compiler is defined for the requested stack in backend configuration", func() {
		BeforeEach(func() {
			stagingRequest.Stack = "no_such_stack"
		})

		It("returns an error", func() {
			_, err := backend.BuildRecipe(stagingRequestJson)

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no compiler defined for requested stack"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL", func() {
		BeforeEach(func() {
			stagingRequest.Stack = "compiler_with_full_url"
		})

		It("uses the full URL in the download tailor action", func() {
			desiredTask, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).ShouldNot(HaveOccurred())

			downloadAction := desiredTask.Action.Action.(models.SerialAction).Actions[0].Action.(models.EmitProgressAction).Action.Action.(models.ParallelAction).Actions[0].Action.(models.EmitProgressAction).Action.Action.(models.DownloadAction)
			Ω(downloadAction.From).Should(Equal("http://the-full-compiler-url"))
		})
	})

	Context("when the compiler for the requested stack is specified as a full URL with an unexpected scheme", func() {
		BeforeEach(func() {
			stagingRequest.Stack = "compiler_with_bad_url"
		})

		It("returns an error", func() {
			_, err := backend.BuildRecipe(stagingRequestJson)
			Ω(err).Should(HaveOccurred())
		})
	})

	Context("when build artifacts download url is not a valid url", func() {
		BeforeEach(func() {
			stagingRequest.BuildArtifactsCacheDownloadUri = "not-a-uri"
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
				"-appDir=/app",
				"-buildArtifactsCacheDir=/tmp/cache",
				"-buildpackOrder=zfirst-buildpack,asecond-buildpack",
				"-buildpacksDir=/tmp/buildpacks",
				"-outputBuildArtifactsCache=/tmp/output-cache",
				"-outputDroplet=/tmp/droplet",
				"-outputMetadata=/tmp/result.json",
				"-skipCertVerify=true",
			}

			desiredTask, err := backend.BuildRecipe(stagingRequestJson)

			Ω(err).ShouldNot(HaveOccurred())
			runAction := desiredTask.Action.Action.(models.SerialAction).Actions[1].Action.(models.EmitProgressAction).Action.Action.(models.RunAction)
			Ω(runAction.Args).Should(Equal(args))
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
							stagingResult := models.StagingResult{
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

func actionsFromExecutorSerialAction(action models.ExecutorAction) []models.ExecutorAction {
	return action.Action.(models.SerialAction).Actions
}
