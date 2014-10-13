package main_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry-incubator/stager/testrunner"
	"github.com/cloudfoundry/gunk/diegonats"
	"github.com/cloudfoundry/storeadapter/storerunner/etcdstorerunner"
	"github.com/onsi/gomega/gexec"
)

func TestStager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Stager Suite")
}

var stagerPath string
var etcdRunner *etcdstorerunner.ETCDClusterRunner
var natsRunner *diegonats.NATSRunner
var runner *testrunner.StagerRunner

var _ = SynchronizedBeforeSuite(func() []byte {
	stager, err := gexec.Build("github.com/cloudfoundry-incubator/stager", "-race")
	Ω(err).ShouldNot(HaveOccurred())
	return []byte(stager)
}, func(stager []byte) {
	stagerPath = string(stager)
})

var _ = SynchronizedAfterSuite(func() {
	if etcdRunner != nil {
		etcdRunner.Stop()
	}
	if natsRunner != nil {
		natsRunner.Stop()
	}
	if runner != nil {
		runner.Stop()
	}
}, func() {
	gexec.CleanupBuildArtifacts()
})