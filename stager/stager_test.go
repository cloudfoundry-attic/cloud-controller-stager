package stager_test

import (
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry/gunk/timeprovider"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"time"
)

var _ = Describe("Stage", func() {
	var stager Stager
	var bbs *Bbs.BBS

	BeforeEach(func() {
		bbs = Bbs.New(etcdRunner.Adapter(), timeprovider.NewTimeProvider())
		compilers := map[string]string{
			"penguin":     "penguin-compiler",
			"rabbit_hole": "rabbit-hole-compiler",
		}
		stager = New(bbs, compilers)
	})

	Context("when file the server is available", func() {
		BeforeEach(func() {
			_, _, err := bbs.MaintainFileServerPresence(10*time.Second, "http://file-server.com/", "abc123")
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("creates a RunOnce with staging instructions", func(done Done) {
			modelChannel, _, _ := bbs.WatchForDesiredRunOnce()

			err := stager.Stage(models.StagingRequestFromCC{
				AppId:              "bunny",
				TaskId:             "hop",
				AppBitsDownloadUri: "http://example-uri.com/bunny",
				Stack:              "rabbit_hole",
				FileDescriptors:    17,
				MemoryMB:           256,
				DiskMB:             1024,
				Buildpacks: []models.Buildpack{
					{Key: "zfirst-buildpack", Url: "first-buildpack-url"},
					{Key: "asecond-buildpack", Url: "second-buildpack-url"},
				},
				Environment: [][]string{
					{"VCAP_APPLICATION", "foo"},
					{"VCAP_SERVICES", "bar"},
				},
			}, "me")
			Ω(err).ShouldNot(HaveOccurred())

			runOnce := <-modelChannel

			Ω(runOnce.Guid).To(Equal("bunny-hop"))
			Ω(runOnce.ReplyTo).To(Equal("me"))
			Ω(runOnce.Stack).To(Equal("rabbit_hole"))
			Ω(runOnce.Log.Guid).To(Equal("bunny"))
			Ω(runOnce.Log.SourceName).To(Equal("STG"))
			Ω(runOnce.FileDescriptors).To(Equal(17))
			Ω(runOnce.Log.Index).To(BeNil())
			Ω(runOnce.Actions).To(Equal([]models.ExecutorAction{
				{
					models.DownloadAction{
						Name:    "Linux Smelter",
						From:    "http://file-server.com/static/rabbit-hole-compiler",
						To:      "/tmp/compiler",
						Extract: true,
					},
				},
				{
					models.DownloadAction{
						Name:    "App Bits",
						From:    "http://example-uri.com/bunny",
						To:      "/app",
						Extract: true,
					},
				},
				{
					models.DownloadAction{
						Name:    "Buildpack",
						From:    "first-buildpack-url",
						To:      "/tmp/buildpacks/zfirst-buildpack",
						Extract: true,
					},
				},
				{
					models.DownloadAction{
						Name:    "Buildpack",
						From:    "second-buildpack-url",
						To:      "/tmp/buildpacks/asecond-buildpack",
						Extract: true,
					},
				},
				{
					models.RunAction{
						Name: "Staging",
						Script: "/tmp/compiler/run" +
							" -appDir='/app'" +
							" -buildpackOrder='zfirst-buildpack,asecond-buildpack'" +
							" -buildpacksDir='/tmp/buildpacks'" +
							" -cacheDir='/tmp/cache'" +
							" -outputDir='/tmp/droplet'" +
							" -resultDir='/tmp/result'",
						Env: [][]string{
							{"VCAP_APPLICATION", "foo"},
							{"VCAP_SERVICES", "bar"},
						},
						Timeout: 15 * time.Minute,
					},
				},
				{
					models.UploadAction{
						Name: "Droplet",
						From: "/tmp/droplet/droplet.tgz",
						To:   "http://file-server.com/droplet/bunny",
					},
				},
				{
					models.FetchResultAction{
						Name: "Staging Result",
						File: "/tmp/result/result.json",
					},
				},
			}))
			Ω(runOnce.MemoryMB).To(Equal(256))
			Ω(runOnce.DiskMB).To(Equal(1024))

			close(done)
		}, 2)
	})

	Context("when file server is not available", func() {
		It("should return an error", func() {
			err := stager.Stage(models.StagingRequestFromCC{
				AppId:              "bunny",
				TaskId:             "hop",
				AppBitsDownloadUri: "http://example-uri.com/bunny",
				Stack:              "rabbit_hole",
				MemoryMB:           256,
				DiskMB:             1024,
			}, "me")

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no available file server present"))
		})
	})

	Context("when no compiler is defined for the requested stack in stager configuration", func() {
		BeforeEach(func() {
			_, _, err := bbs.MaintainFileServerPresence(10*time.Second, "http://file-server.com/", "abc123")
			Ω(err).ShouldNot(HaveOccurred())
		})

		It("should return an error", func(done Done) {
			bbs.WatchForDesiredRunOnce()

			err := stager.Stage(models.StagingRequestFromCC{
				AppId:              "bunny",
				TaskId:             "hop",
				AppBitsDownloadUri: "http://example-uri.com/bunny",
				Stack:              "no_such_stack",
				MemoryMB:           256,
				DiskMB:             1024,
			}, "me")

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("no compiler defined for requested stack"))
			close(done)
		})
	})
})
