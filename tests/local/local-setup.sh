#!/usr/bin/env bash

kind create cluster --config local-kind.yaml
kubectl apply -f ../../node-reboot-detector.yaml
kubectl apply -f ../../node-reboot-reporter.yaml
kubectl apply -f ../../node-maintenance-scheduler.yaml
kubectl apply -f local-kind-manifests.yaml
kind load docker-image ghcr.io/kubereboot/node-reboot-detector:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/node-reboot-reporter:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/node-maintenance-scheduler:dev  --name kured-local