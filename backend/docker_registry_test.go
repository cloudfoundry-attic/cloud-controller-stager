package backend_test

import (
	"encoding/json"

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
		backend              Backend
	)

	BeforeEach(func() {
		dockerRegistryURL := "http://10.244.2.6:5000"

		config := Config{
			FileServerURL:     "http://file-server.com",
			DockerRegistryURL: dockerRegistryURL,
		}

		logger := lager.NewLogger("fakelogger")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))

		backend = NewDockerBackend(config, logger)

		downloadTailorAction = models.EmitProgressFor(
			&models.DownloadAction{
				From:     "http://file-server.com/v1/static/docker_app_lifecycle.zip",
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
					"-dockerRegistryURL",
					dockerRegistryURL,
				},
				Env: []models.EnvironmentVariable{},
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
			AppId:           "bunny",
			TaskId:          "hop",
			DockerImageUrl:  "busybox",
			Stack:           "rabbit_hole",
			FileDescriptors: 512,
			MemoryMB:        512,
			DiskMB:          512,
			Timeout:         512,
		}

		var err error
		stagingRequestJson, err = json.Marshal(stagingRequest)
		Ω(err).ShouldNot(HaveOccurred())
	})

	It("creates a cf-app-docker-staging Task with staging instructions", func() {
		desiredTask, err := backend.BuildRecipe(stagingRequestJson)
		Ω(err).ShouldNot(HaveOccurred())

		actions := actionsFromDesiredTask(desiredTask)
		Ω(actions).Should(HaveLen(2))
		Ω(actions[0]).Should(Equal(downloadTailorAction))
		Ω(actions[1]).Should(Equal(runAction))
	})
})
