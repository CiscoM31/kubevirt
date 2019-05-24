#!/bin/bash


function up() {
    echo "Not supported by this provider"
}

function build() {
    local SSH_USER=""
    [ -n "$KB_NODE_USER" ] && SSH_USER="-l $KB_NODE_USER"
    # Make sure we have any required variables before starting
    if [ -z ${DOCKER_PREFIX+x} ]; then echo "DOCKER_PREFIX variable not set"; exit 1; fi

    # Build and push things
    make manifests docker publish

    # Then make sure we are using latest on all the nodes, this will require SSH keys to be setup
    for node in `_kubectl get nodes | tail -n +2 | awk '{print $1}'`; do
        echo "Connecting to host $node"
        ssh $SSH_USER $node docker pull $DOCKER_PREFIX/virt-api
        ssh $SSH_USER $node docker pull $DOCKER_PREFIX/virt-handler
        ssh $SSH_USER $node docker pull $DOCKER_PREFIX/virt-launcher
        ssh $SSH_USER $node docker pull $DOCKER_PREFIX/virt-controller
    done

}

function _kubectl() {
    kubectl "$@"
}

function down() {
    echo "Not supported by this provider. Please kill the running script manually."
}
