#!/bin/bash

set -e
token=$1
curl -sS -A 'travis-terraform-provider' --header 'Authorization: Bearer '"$TTS_TOKEN"'' -X DELETE https://tt-service.hetzner.cloud/token?token=''"$token"''