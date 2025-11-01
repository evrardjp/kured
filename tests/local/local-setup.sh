#!/usr/bin/env bash

kind create cluster --config local-kind.yaml
kubectl apply -f ../../node-reboot-detector.yaml
kubectl apply -f ../../node-reboot-reporter.yaml
kubectl delete deployment -n kube-system node-reboot-reporter
kubectl apply -f local-kind-manifests.yaml
kind load docker-image ghcr.io/kubereboot/node-reboot-detector:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/node-reboot-reporter:dev  --name kured-local