package stager_docker_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"testing"
)

func TestStagerDocker(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "StagerDocker Suite")
}
