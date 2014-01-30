package stager_test

import (
	"github.com/cloudfoundry-incubator/runtime-schema/bbs"
	. "github.com/cloudfoundry-incubator/stager/stager"
	"github.com/cloudfoundry/storeadapter/fakestoreadapter"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Stage", func() {
	var stager Stager
	var fauxStoreAdapter *fakestoreadapter.FakeStoreAdapter

	BeforeEach(func() {
		fauxStoreAdapter = fakestoreadapter.New()
		stagerBBS := bbs.New(fauxStoreAdapter)
		stager = NewStager(stagerBBS)
	})

	It("puts a job in etcd", func(done Done) {
		executorBBS := bbs.New(fauxStoreAdapter).ExecutorBBS
		modelChannel, _, _ := executorBBS.WatchForDesiredRunOnce()

		err := stager.Stage(StagingRequest{
			AppId:  "bunny",
			TaskId: "hop",
			Stack:  "rabbit_hole",
		}, "me")
		Ω(err).ShouldNot(HaveOccurred())

		runOnce := <-modelChannel
		Ω(runOnce.Guid).To(Equal("bunny-hop"))
		Ω(runOnce.ReplyTo).To(Equal("me"))
		Ω(runOnce.Stack).To(Equal("rabbit_hole"))

		close(done)
	}, 2)
})
