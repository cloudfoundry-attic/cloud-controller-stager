package stager_docker_test

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudfoundry/storeadapter"
	"github.com/pivotal-golang/lager"

	"github.com/cloudfoundry-incubator/receptor"
	"github.com/cloudfoundry-incubator/receptor/fake_receptor"
	"github.com/cloudfoundry-incubator/runtime-schema/bbs/fake_bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/cc_messages"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	"github.com/cloudfoundry-incubator/stager/stager"
	. "github.com/cloudfoundry-incubator/stager/stager_docker"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("StagerDocker", func() {
	var (
		bbs                  *fake_bbs.FakeStagerBBS
		stagingRequest       cc_messages.DockerStagingRequestFromCC
		downloadTailorAction models.ExecutorAction
		runAction            models.ExecutorAction
		config               stager.Config
		fakeDiegoAPIClient   *fake_receptor.FakeClient
		callbackURL          string
		dockerStager         DockerStager
	)

	BeforeEach(func() {
		fakeDiegoAPIClient = new(fake_receptor.FakeClient)
		bbs = &fake_bbs.FakeStagerBBS{}
		logger := lager.NewLogger("fakelogger")

		callbackURL = "http://the-stager.example.com"

		config = stager.Config{
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

		dockerStager = New(bbs, callbackURL, fakeDiegoAPIClient, logger, config)

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

	Context("when file the server is available", func() {
		BeforeEach(func() {
			bbs.GetAvailableFileServerReturns("http://file-server.com/", nil)
		})

		It("creates a cf-app-docker-staging Task with staging instructions", func() {
			err := dockerStager.Stage(stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			desiredTask := fakeDiegoAPIClient.CreateTaskArgsForCall(0)

			Ω(desiredTask.Domain).To(Equal("cf-app-docker-staging"))
			Ω(desiredTask.TaskGuid).To(Equal("bunny-hop"))
			Ω(desiredTask.Stack).To(Equal("rabbit_hole"))
			Ω(desiredTask.Log.Guid).To(Equal("bunny"))
			Ω(desiredTask.Log.SourceName).To(Equal("STG"))
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
			err := dockerStager.Stage(stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			desiredTask := fakeDiegoAPIClient.CreateTaskArgsForCall(0)
			Ω(desiredTask.CompletionCallbackURL).Should(Equal(callbackURL))
		})

		Describe("resource limits", func() {
			Context("when the app's memory limit is less than the minimum memory", func() {
				BeforeEach(func() {
					stagingRequest.MemoryMB = 256
				})

				It("uses the minimum memory", func() {
					err := dockerStager.Stage(stagingRequest)
					Ω(err).ShouldNot(HaveOccurred())

					desiredTask := fakeDiegoAPIClient.CreateTaskArgsForCall(0)
					Ω(desiredTask.MemoryMB).Should(BeNumerically("==", config.MinMemoryMB))
				})
			})

			Context("when the app's disk limit is less than the minimum disk", func() {
				BeforeEach(func() {
					stagingRequest.DiskMB = 256
				})

				It("uses the minimum disk", func() {
					err := dockerStager.Stage(stagingRequest)
					Ω(err).ShouldNot(HaveOccurred())

					desiredTask := fakeDiegoAPIClient.CreateTaskArgsForCall(0)
					Ω(desiredTask.DiskMB).Should(BeNumerically("==", config.MinDiskMB))
				})
			})

			Context("when the app's memory limit is less than the minimum memory", func() {
				BeforeEach(func() {
					stagingRequest.FileDescriptors = 17
				})

				It("uses the minimum file descriptors", func() {
					err := dockerStager.Stage(stagingRequest)
					Ω(err).ShouldNot(HaveOccurred())

					desiredTask := fakeDiegoAPIClient.CreateTaskArgsForCall(0)

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

		Context("when the task has already been created", func() {
			BeforeEach(func() {
				fakeDiegoAPIClient.CreateTaskReturns(receptor.Error{
					Type:    receptor.TaskGuidAlreadyExists,
					Message: "ok, this task already exists",
				})
			})

			It("does not raise an error", func() {
				err := dockerStager.Stage(stagingRequest)
				Ω(err).ShouldNot(HaveOccurred())
			})
		})

		Context("when the API call fails", func() {
			desireErr := errors.New("Could not connect!")

			BeforeEach(func() {
				fakeDiegoAPIClient.CreateTaskReturns(desireErr)
			})

			It("returns an error", func() {
				err := dockerStager.Stage(stagingRequest)
				Ω(err).Should(Equal(desireErr))
			})
		})
	})

	Context("when file server is not available", func() {
		BeforeEach(func() {
			bbs.GetAvailableFileServerReturns("http://file-server.com/", storeadapter.ErrorKeyNotFound)
		})

		It("should return an error", func() {
			err := dockerStager.Stage(cc_messages.DockerStagingRequestFromCC{
				AppId:          "bunny",
				TaskId:         "hop",
				DockerImageUrl: "the-image",
				Stack:          "rabbit_hole",
				MemoryMB:       256,
				DiskMB:         1024,
			})

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no available file server present"))
		})
	})
})
