package stager_test

import (
	"os"
	"os/signal"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestExecutor(t *testing.T) {
	registerSignalHandler()
	RegisterFailHandler(Fail)

	RunSpecs(t, "Stager Suite")
}

func registerSignalHandler() {
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, os.Kill)

		select {
		case <-c:
			os.Exit(0)
		}
	}()
}
