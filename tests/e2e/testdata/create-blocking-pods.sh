#!/usr/bin/env bash

kubectl_flags=( )
[[ "$1" != "" ]] && kubectl_flags=("${kubectl_flags[@]}" --context "$1")

# To speed up the system, let's not kill the control plane.
for node in $(${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" get nodes -o name | grep -v control-plane); do
    # Extract just the node name without the "node/" prefix
    nodename="${node#node/}"
    
    # Create a blocking pod with PodDisruptionBudget to prevent draining
    echo "Creating blocking pod on $node..."
    ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" apply -f - << EOF
apiVersion: v1
kind: Pod
metadata:
  name: blocking-pod-${nodename}
  labels:
    app: blocker-${nodename}
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      imagePullPolicy: IfNotPresent
  nodeSelector:
    kubernetes.io/hostname: "${nodename}"
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: blocking-pdb-${nodename}
spec:
  minAvailable: 1
  selector:
    matchLabels:
      app: blocker-${nodename}
EOF

    # Wait for pod to be ready
    echo "Waiting for blocking pod to be ready..."
    ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" wait --for=condition=Ready pod/blocking-pod-${nodename} --timeout=60s
done
