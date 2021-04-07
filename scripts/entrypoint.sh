#!/bin/sh

set -e

if [ -z "${POD_NAME}" ]; then
  echo "POD_NAME must be set" 1>&2
  exit 1
fi

if [ -z "${RUNNER_TOKEN}" ]; then
  echo "RUNNER_TOKEN must be set" 1>&2
  exit 1
fi

if [ -z "${RUNNER_ORG}" ]; then
  echo "RUNNER_ORG must be set" 1>&2
  exit 1
fi

if [ -z "${RUNNER_REPO}" ]; then
  echo "RUNNER_REPO must be set" 1>&2
  exit 1
fi

cd /runner
mkdir -p _work
./config.sh --unattended --replace --name "${POD_NAME}" --url "https://github.com/${RUNNER_ORG}/${RUNNER_REPO}" --token "${RUNNER_TOKEN}" --work /runner/_work

# TODO: run placemat

./bin/runsvc.sh
[ -f /tmp/github.env ] && . /tmp/github.env

if [ -z "${EXTEND_DURATION}" ]; then
  EXTEND_DURATION="20m"
fi

if [ -f /tmp/failed ]; then
  echo "Annotate pods with the time ${EXTEND_DURATION} later"
  deltime-annotate ${POD_NAME} -a ${EXTEND_DURATION}

  if [ -n "${SLACK_AGENT_SERVICE_NAME}" ]; then
    echo "Send an notification to slack that CI failed"
    slack-agent client -n ${POD_NAMESPACE} ${POD_NAME} \
      --workflow ${WORKFLOW_NAME} \
      --branch ${BRANCH_NAME} \
      --organization ${RUNNER_ORG} \
      --repository ${RUNNER_REPO} \
      --run-id ${RUN_ID} \
      --notifier-address ${SLACK_AGENT_SERVICE_NAME} \
      --failed
  else
    echo "Skip sending an notification to slack because SLACK_AGENT_SERVICE_NAME is blank"
  fi
else
  echo "Annotate pods with current time"
  deltime-annotate ${POD_NAME}

  if [ -n "${SLACK_AGENT_SERVICE_NAME}" ]; then
    echo "Send an notification to slack that CI failed"
    slack-agent client -n ${POD_NAMESPACE} ${POD_NAME} \
      --workflow ${WORKFLOW_NAME} \
      --branch ${BRANCH_NAME} \
      --organization ${RUNNER_ORG} \
      --repository ${RUNNER_REPO} \
      --run-id ${RUN_ID} \
      --notifier-address ${SLACK_AGENT_SERVICE_NAME}
  else
    echo "Skip sending an notification to slack because SLACK_AGENT_SERVICE_NAME is blank"
  fi
fi
sleep infinity

