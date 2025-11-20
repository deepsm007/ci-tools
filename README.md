# CI-Tools

A collection of tools and utilities for managing and operating the OpenShift CI (Continuous Integration) platform. This repository contains command-line tools, controllers, webhooks, and services that automate CI configuration management, job generation, repository onboarding, and various operational tasks.

## Overview

CI-Tools provides a comprehensive set of utilities that work together to maintain and operate the OpenShift CI infrastructure. These tools handle tasks such as:

- **CI Configuration Management**: Generate, validate, and maintain CI operator configurations
- **Job Generation**: Automatically create Prow job configurations from CI operator configs
- **Repository Onboarding**: Streamline the process of adding new repositories to the CI platform
- **Config Synchronization**: Mirror and propagate configuration changes across branches and repositories
- **Secret Management**: Handle secrets, credentials, and sensitive configuration data
- **Cluster Management**: Tools for cluster initialization, monitoring, and maintenance
- **Build & Image Management**: Manage container images, registries, and build processes
- **Testing Infrastructure**: Support for test execution, result aggregation, and reporting

## Key Components

### Configuration Tools
- `ci-operator-prowgen` - Generates Prow job configurations from CI operator configs
- `config-brancher` - Propagates CI configurations across release branches
- `ci-operator-yaml-creator` - Creates and validates CI operator YAML configurations
- `autoconfigbrancher` - Automates CI configuration reconciliation

### Repository Management
- `repo-init` - Web-based tool for onboarding new repositories to the CI platform
- `repo-brancher` - Manages repository branching and configuration duplication

### Operational Tools
- `cluster-init` - Initializes and configures OpenShift clusters
- `pod-scaler` - Manages pod scaling and resource allocation
- `job-run-aggregator` - Aggregates and analyzes CI job run results
- `vault-secret-collection-manager` - Manages secrets in Vault

### CI/CD Integration
- `prow-job-dispatcher` - Dispatches Prow jobs to appropriate clusters
- `pipeline-controller` - Manages CI/CD pipelines
- `retester` - Automatically retests failed jobs

## Documentation

For detailed documentation, see the [docs](docs/) directory:

- **[Documentation Index](docs/README.md)** - Complete documentation guide
- **[OpenShift Documentation](docs/openshift/)** - OpenShift platform architecture and guides
- **[Contributing Guide](CONTRIBUTING.md)** - How to contribute to this project

## Getting Started

1. **Clone the repository**:
   ```bash
   git clone https://github.com/openshift/ci-tools.git
   cd ci-tools
   ```

2. **Build the tools**:
   ```bash
   make build
   ```

3. **Run tests**:
   ```bash
   make test
   ```

4. **Explore individual tools**: Each tool in the `cmd/` directory has its own README with usage instructions.

## External Resources

- [OpenShift CI Documentation](https://docs.ci.openshift.org/) - Official OpenShift CI documentation
- [Prow Documentation](https://github.com/kubernetes/test-infra/tree/master/prow) - Prow CI system documentation
- [CI Operator Reference](https://steps.svc.ci.openshift.org/) - CI Operator step registry

## License

This project is licensed under the Apache-2.0 license. See the [LICENSE](LICENSE) file for details.

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to contribute to this project.
