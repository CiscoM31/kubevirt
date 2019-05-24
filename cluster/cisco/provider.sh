#!/bin/bash


function up() {
    echo "Not supported by this provider"
}

function build() {
    make manifests docker
}

function _kubectl() {
    kubectl "$@"
}

function down() {
    echo "Not supported by this provider. Please kill the running script manually."
}
