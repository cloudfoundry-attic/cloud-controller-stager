package stager_test

import (
	"encoding/json"
	. "github.com/cloudfoundry-incubator/stager/stager"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("StagingMessages", func() {
	Describe("StagingRequest", func() {
		ccJSON := `{
           "app_id" : "fake-app_id",
           "task_id" : "fake-task_id",
           "memoryMB" : 1024,
           "diskMB" : 10000,
           "fileDescriptors" : "fake-fileDescriptors",
           "environment" : [["FOO", "BAR"]],
           "stack" : "fake-stack",
           "download_uri" : "fake-download_uri",
           "upload_uri" : "fake-upload_uri",
           "buildpack_cache_download_uri" : "fake-buildpack_cache_download_uri",
           "buildpack_cache_upload_uri" : "fake-buildpack_cache_upload_uri",
           "admin_buildpacks" : [{"key":"fake-buildpack-key" ,"url":"fake-buildpack-url"}]
        }`

		It("should be mapped to the CC's staging request JSON", func() {
			var stagingRequest StagingRequest
			err := json.Unmarshal([]byte(ccJSON), &stagingRequest)
			Ω(err).ShouldNot(HaveOccurred())

			Ω(stagingRequest).Should(Equal(StagingRequest{
				AppId:       "fake-app_id",
				TaskId:      "fake-task_id",
				Stack:       "fake-stack",
				DownloadUri: "fake-download_uri",
				UploadUri:   "fake-upload_uri",
				MemoryMB:    1024,
				DiskMB:      10000,
				AdminBuildpacks: []AdminBuildpack{
					{
						Key: "fake-buildpack-key",
						Url: "fake-buildpack-url",
					},
				},
				Environment: [][]string{
					{"FOO", "BAR"},
				},
			}))
		})
	})
})
