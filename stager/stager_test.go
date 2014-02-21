package stager_test

import (
	Bbs "github.com/cloudfoundry-incubator/runtime-schema/bbs"
	. "github.com/cloudfoundry-incubator/runtime-schema/models"
	. "github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry/storeadapter/fakestoreadapter"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"path"
	"time"
)

var _ = Describe("Stage", func() {
	var stager Stager
	var bbs *Bbs.BBS
	var fauxStoreAdapter *fakestoreadapter.FakeStoreAdapter

	BeforeEach(func() {
		fauxStoreAdapter = fakestoreadapter.New()
		bbs = Bbs.New(fauxStoreAdapter)
		compilers := map[string]string{
			"penguin":     "penguin-compiler",
			"rabbit_hole": "rabbit-hole-compiler",
		}
		stager = NewStager(bbs, compilers)
	})

	Context("when file server is not available", func() {
		It("should return an error", func() {
			err := stager.Stage(StagingRequest{
				AppId:       "bunny",
				TaskId:      "hop",
				DownloadUri: "http://example-uri.com/bunny",
				Stack:       "rabbit_hole",
				MemoryMB:    256,
				DiskMB:      1024,
			}, "me")

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("No available file server present"))
		})
	})

	Context("when file server is available", func() {
		BeforeEach(func() {
			bbs.MaintainFileServerPresence(10, "hello.com", "abc123")
		})

		It("creates a RunOnce with an instruction to download the app bits", func(done Done) {
			modelChannel, _, _ := bbs.WatchForDesiredRunOnce()

			err := stager.Stage(StagingRequest{
				AppId:       "bunny",
				TaskId:      "hop",
				DownloadUri: "http://example-uri.com/bunny",
				Stack:       "rabbit_hole",
				MemoryMB:    256,
				DiskMB:      1024,
				AdminBuildpacks: []AdminBuildpack{
					AdminBuildpack{Key: "zfirst-buildpack", Url: "first-buildpack-url"},
					AdminBuildpack{Key: "asecond-buildpack", Url: "second-buildpack-url"},
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
			Ω(runOnce.Log.Index).To(BeNil())
			Ω(runOnce.Actions).To(Equal([]ExecutorAction{
				{
					DownloadAction{
						From:    path.Join("hello.com", "rabbit-hole-compiler"),
						To:      "/tmp/compiler",
						Extract: true,
					},
				},
				{
					DownloadAction{
						From:    "http://example-uri.com/bunny",
						To:      "/tmp/app",
						Extract: true,
					},
				},
				{
					DownloadAction{
						From:    "first-buildpack-url",
						To:      "/tmp/buildpacks/zfirst-buildpack",
						Extract: true,
					},
				},
				{
					DownloadAction{
						From:    "second-buildpack-url",
						To:      "/tmp/buildpacks/asecond-buildpack",
						Extract: true,
					},
				},
				{
					RunAction{
						Script: "/tmp/compiler/run",
						Env: [][]string{
							{"VCAP_APPLICATION", "foo"},
							{"VCAP_SERVICES", "bar"},
							{"APP_DIR", "/tmp/app"},
							{"OUTPUT_DIR", "/tmp/droplet"},
							{"BUILDPACKS_DIR", "/tmp/buildpacks"},
							{"BUILDPACK_ORDER", `["zfirst-buildpack","asecond-buildpack"]`},
							{"CACHE_DIR", "/tmp/cache"},
						},
						Timeout: 15 * time.Minute,
					},
				},
			}))
			Ω(runOnce.MemoryMB).To(Equal(256))
			Ω(runOnce.DiskMB).To(Equal(1024))

			close(done)
		}, 2)
	})

	Context("when no compiler is defined for the requested stack in stager configuration", func() {
		It("should return an error", func(done Done) {
			bbs.WatchForDesiredRunOnce()

			err := stager.Stage(StagingRequest{
				AppId:       "bunny",
				TaskId:      "hop",
				DownloadUri: "http://example-uri.com/bunny",
				Stack:       "no_such_stack",
				MemoryMB:    256,
				DiskMB:      1024,
			}, "me")

			Ω(err).Should(HaveOccurred())
			Ω(err.Error()).Should(Equal("No compiler defined for requested stack"))
			close(done)
		})
	})
})
