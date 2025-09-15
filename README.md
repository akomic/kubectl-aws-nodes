# kubectl-aws-nodes

A kubectl plugin that extends `kubectl get nodes` with AWS EC2 instance information.

## Features

- Lists Kubernetes nodes with standard information (name, status, roles, age, version)
- Adds AWS EC2 instance ID and instance type for each node
- Works with any Kubernetes cluster running on AWS EC2

## Prerequisites

- Go 1.23+
- kubectl configured to access your cluster
- AWS credentials configured (via AWS CLI, environment variables, or IAM roles)
- EC2 read permissions for the AWS account

## Installation

1. Clone and build:
```bash
git clone https://github.com/akomic/kubectl-aws-nodes
cd kubectl-aws-nodes
make build
```

2. Install the plugin:
```bash
make install
```

Or manually copy the binary:
```bash
cp bin/kubectl-aws_nodes /usr/local/bin/
```

## Usage

```bash
kubectl nodes
```

For detailed resource information:
```bash
kubectl nodes -o wide
```

For resource-focused view:
```bash
kubectl nodes -o top
```

## Output

The plugin outputs a table with the following columns:
- **NAME**: Node name
- **STATUS**: Node status (Ready/NotReady)
- **AGE**: Time since node creation
- **VERSION**: Kubelet version
- **INSTANCE-ID**: AWS EC2 instance ID
- **INSTANCE-TYPE**: AWS EC2 instance type
- **NODEGROUP**: EKS node group name (from eks:nodegroup-name tag)

With `-o wide`, additional resource columns are shown:
- **CPU-CAP**: CPU capacity (allocatable)
- **CPU-REQ**: CPU requested by pods
- **CPU-FREE%**: Percentage of CPU not requested
- **MEM-CAP**: Memory capacity (allocatable)
- **MEM-REQ**: Memory requested by pods
- **MEM-FREE%**: Percentage of memory not requested

With `-o top`, only resource-focused columns are shown:
- **NAME**: Node name
- **PODS**: Number of pods running on the node
- **CPU-CAP**: CPU capacity (allocatable)
- **CPU-REQ**: CPU requested by pods
- **CPU-FREE%**: Percentage of CPU not requested
- **MEM-CAP**: Memory capacity (allocatable)
- **MEM-REQ**: Memory requested by pods
- **MEM-FREE%**: Percentage of memory not requested

## Example Output

```
NAME                                          STATUS   AGE   VERSION   INSTANCE-ID         INSTANCE-TYPE  NODEGROUP
ip-10-0-1-100.us-west-2.compute.internal     Ready    5d    v1.28.0   i-0123456789abcdef0  m5.large      worker-nodes
ip-10-0-2-200.us-west-2.compute.internal     Ready    5d    v1.28.0   i-0987654321fedcba0  m5.xlarge     worker-nodes
```

## How it works

The plugin:
1. Connects to your Kubernetes cluster using your current kubectl context
2. Retrieves node information via the Kubernetes API
3. Extracts EC2 instance IDs from node `spec.providerID` fields
4. Queries AWS EC2 API to get instance details
5. Combines and displays the information in a table format
