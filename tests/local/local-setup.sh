#!/usr/bin/env bash

#kind create cluster --config local-kind.yaml
kubectl apply -f maintenance-scheduler.yaml
kubectl apply -f reboot-daemon.yaml
kubectl apply -f reboot-inhibitors.yaml
kubectl apply -f reboot-required-detector.yaml
kubectl apply -f tests/local/local-kind-manifests.yaml
kind load docker-image ghcr.io/kubereboot/maintenance-scheduler:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/reboot-daemon:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/reboot-inhibitors:dev  --name kured-local
kind load docker-image ghcr.io/kubereboot/reboot-required-detector:dev  --name kured-local