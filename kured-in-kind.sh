#!/bin/sh

###############
# User config #
###############

# How to use this?
# Define env vars KIND_IMAGE, KIND_CLUSTER_NAME (or let them be the default),
# and the KURED_IMAGE_DEST (the public repo which will contain the dev version
# of the image to test).
# The process will build kured (using make image), tag the image with
# KURED_IMAGE_DEST, publish it somewhere, spin up a kind cluster, install
# kured in it, create a reboot sentinel file, track if reboot is happening
# by watching the kubernetes api.
# The resources files/kind cluster are destroyed at the end of the process
# unless DO_NOT_TEARDOWN is not empty
# For CI, you should probably set -x before running this script.

#Define the image used in kind, for example:
#KIND_IMAGE=kindest/node:v1.18.2
#KIND_IMAGE=kindest/node:v1.18.0@sha256:0e20578828edd939d25eb98496a685c76c98d54084932f76069f886ec315d694
#KIND_IMAGE=kindest/node:v1.17.0@sha256:9512edae126da271b66b990b6fff768fbb7cd786c7d39e86bdf55906352fdf62
#KIND_IMAGE=kindest/node:v1.16.4@sha256:b91a2c2317a000f3a783489dfb755064177dbc3a0b2f4147d50f04825d016f55
KIND_IMAGE=${KIND_IMAGE:-"kindest/node:v1.18.2"}

# desired cluster name; default is "kured"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-kured}"

KURED_IMAGE_DEST=${KURED_IMAGE_DEST:-"docker.io/evrardjp/kured:latest"}

#############
# Functions #
#############

kubectl="kubectl --context kind-$KIND_CLUSTER_NAME"
# change nodecount if you change the default kind cluster size
nodecount=5

cleanup_tmpfiles() {
	echo "Cleaning up kured tmp folders"
	rm -rf /tmp/kured-*
}

teardown_kindcluster() {
	echo "Tearing down the kind cluster $KIND_CLUSTER_NAME"
	kind delete cluster --name $KIND_CLUSTER_NAME
}

cleanup() {
        if [ -z "$DO_NOT_TEARDOWN" ]; then
		cleanup_tmpfiles
		teardown_kindcluster
        fi
}

retry() {
    local -r -i max_attempts="$1"; shift
    local -r cmd="$@"
    local -i attempt_num=1

    until $cmd
    do
        if (( attempt_num == max_attempts ))
        then
            echo "Attempt $attempt_num failed and there are no more attempts left!"
            return 1
        else
            echo "Attempt $attempt_num failed! Trying again in $attempt_num seconds..."
            sleep $(( attempt_num++ ))
        fi
    done
}
####################
# Kured build step #
####################

build_and_push_kuredimage() {
	make image
        docker tag docker.io/weaveworks/kured $KURED_IMAGE_DEST
        docker push $KURED_IMAGE_DEST
}

############################
# Kind cluster create step #
############################

gen_kind_manifest() {
        echo "Generating kind.yaml"
	cat <<EOF > $tmp_dir/kind.yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: $KIND_IMAGE
- role: control-plane
  image: $KIND_IMAGE
- role: control-plane
  image: $KIND_IMAGE
- role: worker
  image: $KIND_IMAGE
- role: worker
  image: $KIND_IMAGE
EOF
}

check_all_nodes_are_ready() {
	$kubectl get nodes | grep Ready | wc -l | grep $nodecount
}

spinup_cluster() {
	gen_kind_manifest
	kind create cluster --name "${KIND_CLUSTER_NAME}" --config=$tmp_dir/kind.yaml
	retry 10 check_all_nodes_are_ready
}


######################
# Kured install step #
######################

gen_kured_manifests() {
	echo "Generating kured manifests in folder $tmp_dir"
	cat <<EOF > $tmp_dir/kustomize.yaml
#kustomize.yaml base
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://raw.githubusercontent.com/weaveworks/kured/master/kured-ds.yaml
  - https://raw.githubusercontent.com/weaveworks/kured/master/kured-rbac.yaml
patchesStrategicMerge:
  - kured-dev.yaml
EOF

	cat <<EOF > $tmp_dir/kured-dev.yaml
# kured-dev.yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kured
  namespace: kube-system
spec:
  template:
    spec:
      containers:
      - name: kured
        image: $KURED_IMAGE_DEST
        imagePullPolicy: Always
        command:
        - /usr/bin/kured
        - --period=1m
EOF
}

check_kured_installed(){
   $kubectl get ds -n kube-system | egrep "kured.*$nodecount.*$nodecount.*$nodecount.*$nodecount.*$nodecount"
}


install_kured() {
	gen_kured_manifests
        $kubectl apply -k $tmp_dir/kustomize.yaml
        retry 10 check_kured_installed
}

######################
# Restart nodes step #
######################

create_reboot_sentinel() {
	echo "TODO"
}

restart_nodes() {

}

gather_logs() {
	kind export logs
	$kubectl logs daemonset.apps/kured -n kube-system
}

########
# MAIN #
########


trap 'cleanup' ERR EXIT

tmp_dir=$(mktemp -d -t kured-XXXX)

build_and_push_kuredimage
spinup_cluster
install_kured
restart_nodes

gather_logs
cleanup
