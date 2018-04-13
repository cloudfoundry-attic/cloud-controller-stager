stager
======
# This repository is now deprecated. 
Staging on Diego is now handled in the cloud_controller_ng repository.

**Note**: This repository should be imported as `code.cloudfoundry.org/stager`.

Diego Stager

####Learn more about Diego and its components at [diego-design-notes](https://github.com/cloudfoundry-incubator/diego-design-notes)

## Running Ginkgo Tests

1. Ensure that you have `consul` version ~> 0.7.0 installed and available on your path (or whatever version is used in the version of [consul-release](https://github.com/cloudfoundry-incubator/consul-release) that is being consumed by [cf-release](https://github.com/cloudfoundry/cf-release/tree/master/src))
1. Install [ginkgo](https://github.com/onsi/ginkgo)
1. Run `ginkgo -r` in the project root
