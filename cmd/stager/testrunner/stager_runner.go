package testrunner

import (
	"os/exec"
	"time"

	"github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
)

type StagerRunner struct {
	Config      Config
	CompilerUrl string
	session     *gexec.Session
}

type Config struct {
	StagerBin   string
	StagerURL   string
	DiegoAPIURL string
	CCBaseURL   string
}

func New(config Config) *StagerRunner {
	return &StagerRunner{
		Config: config,
	}
}

func (r *StagerRunner) Start(args ...string) {
	if r.session != nil {
		panic("starting more than one stager runner!!!")
	}

	stagerSession, err := gexec.Start(
		exec.Command(
			r.Config.StagerBin,
			append([]string{
				"-diegoAPIURL", r.Config.DiegoAPIURL,
				"-stagerURL", r.Config.StagerURL,
				"-ccBaseURL", r.Config.CCBaseURL,
			}, args...)...,
		),
		gexec.NewPrefixedWriter("\x1b[32m[o]\x1b[95m[stager]\x1b[0m ", ginkgo.GinkgoWriter),
		gexec.NewPrefixedWriter("\x1b[91m[e]\x1b[95m[stager]\x1b[0m ", ginkgo.GinkgoWriter),
	)

	Ω(err).ShouldNot(HaveOccurred())
	Eventually(stagerSession).Should(gbytes.Say("Listening for staging requests!"))

	r.session = stagerSession
}

func (r *StagerRunner) Stop() {
	if r.session != nil {
		r.session.Interrupt().Wait(5 * time.Second)
		r.session = nil
	}
}

func (r *StagerRunner) KillWithFire() {
	if r.session != nil {
		r.session.Kill().Wait(5 * time.Second)
		r.session = nil
	}
}

func (r *StagerRunner) Session() *gexec.Session {
	return r.session
}
