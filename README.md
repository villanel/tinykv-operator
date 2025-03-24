# TinyKV

**TinyKV** is a high-performance distributed cache server written in Go. It is designed for consistency, scalability, and Redis protocol compatibility in a distributed environment.

## ✨ Features

- **Raft-Based Consensus**: Ensures strong consistency and fault tolerance using the Raft protocol.
- **Multi-Raft Architecture**: Supports multiple Raft groups to overcome the performance bottleneck of a single Raft, allowing efficient handling of concurrent client requests.
- **Key-Aware Load Balancing**: Integrates with TinySchedule to help clients locate the region of a given key, enabling dynamic load balancing across the cluster.
- **RESP Protocol Support**: Fully implements the Redis Serialization Protocol (RESP), supporting all standard Redis clients out-of-the-box.
- **Region Balancing**: Implements region-balancing algorithms in TinySchedule by adjusting peer and leader placement across nodes to achieve balanced data and network usage.

## Base Works


tinykv is adapted and extended from the following open-source project:

- [TinyKV Course (Talent Plan)](https://github.com/talent-plan/tinykv)
- [RedisGO GitHub Repository](https://github.com/innovationb1ue/RedisGO/tree/Main)

## TinyKV Operator

The **TinyKV Operator** is a Kubernetes custom controller designed to manage distributed key-value store clusters composed of **TinyKV** and **TinySchedule** components. It simplifies the deployment, scaling, and maintenance of these clusters within a Kubernetes environment.

### Description

#### **Two-Phase Deployment**

The operator adopts a sequential, two-phase deployment strategy to ensure a stable setup:

- **Control Plane**: Deploys TinySchedule first to establish the scheduling and load-balancing framework.
- **Data Plane and Client**: Deploys the TinyKV data plane followed by the TinyKV client, ensuring the control plane is fully operational before the data layer and client components are initialized.

This phased approach guarantees correct configuration and minimizes potential runtime issues.

#### **Stateful Management**

The TinyKV Operator provides robust mechanisms for managing the stateful aspects of the cluster:
- **Scaling Up**: Ensures smooth cluster expansion by integrating new nodes without conflicts. TinySchedule will subtly adjust leaders and region placement to maintain balance and effectiveness.
- **Scaling Down**: Carefully removes storage nodes from the scheduling system, avoiding disruptions to the Raft consensus mechanism (e.g., leader downtime or unnecessary leader elections). It also cleans up associated metadata to prevent ID duplication during future scaling operations.
- **Resource-Aware Scaling**: Monitors cluster load and leverages **Horizontal Pod Autoscaling (HPA)** to dynamically adjust the number of pods based on demand.

> **Note:** A planned enhancement (currently marked as TODO) involves leveraging native TinySchedule information to improve the accuracy and efficiency of scaling triggers.

## Getting Started

### Prerequisites
- go version v1.23.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=villanel/tinykv-operator:latest
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=villanel/tinykv-operator:latest
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=villanel/tinykv-operator:latest
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/tinykv-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

# tinykv-operator
