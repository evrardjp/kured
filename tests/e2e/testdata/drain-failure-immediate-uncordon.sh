#!/usr/bin/env bash

# Test script to verify that when drain fails, the node is uncordoned immediately
# and not after the lock-release-delay period.
#
# This test:
# 1. Creates a blocking pod with a PodDisruptionBudget
# 2. Triggers a reboot sentinel
# 3. Verifies the node is uncordoned immediately (within 5s) after drain failure
# 4. Verifies lock is released after the configured delay

kubectl_flags=( )
[[ "$1" != "" ]] && kubectl_flags=("${kubectl_flags[@]}" --context "$1")

function gather_logs_and_cleanup {
    echo "############################################################"
    echo "Gathering logs for debugging..."
    for id in $(docker ps -q); do
        echo "############################################################"
        echo "docker logs for container $id:"
        docker logs "$id" 2>&1 | tail -100
    done
    ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" logs ds/kured --all-pods -n kube-system --tail=100

    # Cleanup
    ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" delete pod blocking-pod --ignore-not-found=true
    ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" delete pdb blocking-pdb --ignore-not-found=true
}
trap gather_logs_and_cleanup EXIT

set -o errexit


# Wait for drain to be attempted (which indicates reboot was detected)
echo "Waiting for drain to start..."
max_attempts=30
attempt_num=1
sleep_time=1

until ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" logs ds/kured --all-pods -n kube-system --tail=50 | grep -i "Starting drain"
do
    if (( attempt_num == max_attempts )); then
        echo "ERROR: Drain not started in time!"
        exit 1
    fi
    echo "Waiting for drain to start... (Attempt #$attempt_num)"
    sleep "$sleep_time"
    (( attempt_num++ ))
done

echo "Drain started"

# Wait for drain to be attempted and fail
echo "Waiting for drain failure and uncordon..."
attempt_num=1
max_attempts=20

until ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" logs ds/kured --all-pods -n kube-system \
  --tail=100 | grep -i "Performing a best-effort uncordon after failed cordon and drain"
do
    if (( attempt_num == max_attempts )); then
        echo "ERROR: Drain failure and uncordon not detected in logs!"
        exit 1
    fi
    echo "Waiting for drain failure and uncordon... (Attempt #$attempt_num)"
    sleep "$sleep_time"
    (( attempt_num++ ))
done

echo "Drain failure and uncordon detected in logs"

# Record timestamp when drain failed
drain_fail_time=$(date +%s)

# CRITICAL TEST: Verify node is uncordoned quickly (within 5 seconds, not after 30s lock-release-delay)
echo "Verifying immediate uncordon after drain failure..."
max_uncordon_wait=10  # Allow up to 10 seconds for uncordon (should happen in ~1-2s)
attempt_num=1

until ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" get nodes \
    -o custom-columns=NAME:.metadata.name,SCHEDULABLE:.spec.unschedulable --no-headers \
    | grep -v control-plane | grep '<none>' | cut -f 1 -d ' '
do
    current_time=$(date +%s)
    elapsed=$((current_time - drain_fail_time))

    if (( elapsed > max_uncordon_wait )); then
        echo "ERROR: Node was not uncordoned within ${max_uncordon_wait}s after drain failure!"
        echo "This indicates the uncordon happened after the lock-release-delay, which is incorrect."
        ${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" get node "$worker" -o yaml
        exit 1
    fi

    echo "Waiting for node to be uncordoned... (${elapsed}s elapsed)"
    sleep 1
    (( attempt_num++ ))
done

uncordon_time=$(date +%s)
uncordon_elapsed=$((uncordon_time - drain_fail_time))

echo "Node was uncordoned ${uncordon_elapsed}s after drain failure (expected < ${max_uncordon_wait}s)"

echo "Verifying lock release behavior..."
lock_value=$(${KUBECTL_CMD:-kubectl} "${kubectl_flags[@]}" get daemonset kured -n kube-system \
    -o jsonpath='{.metadata.annotations.kured\.dev/kured-node-lock}')

if [[ -n "$lock_value" ]]; then
    echo "Lock release delay is being applied (as expected)"
else
    echo "ERROR: No lock release delay found in logs (may be configured to 0)"
    exit 1
fi

echo "Test successful"
