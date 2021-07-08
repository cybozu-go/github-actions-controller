package kindtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/runner/client"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-github/v33/github"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

var _ = kubectlWithInput

func kubectl(args ...string) ([]byte, []byte, error) {
	return execAtLocal(filepath.Join(binDir, "kubectl"), nil, args...)
}

func kubectlWithInput(input []byte, args ...string) ([]byte, []byte, error) {
	return execAtLocal(filepath.Join(binDir, "kubectl"), input, args...)
}

func kubectlSafe(args ...string) []byte {
	stdout, stderr, err := kubectl(args...)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred(), "stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	return stdout
}

func kubectlSafeWithInput(input []byte, args ...string) []byte {
	stdout, stderr, err := kubectlWithInput(input, args...)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred(), "stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	return stdout
}

func kustomizeBuild(dir string) ([]byte, []byte, error) {
	return execAtLocal(filepath.Join(binDir, "kustomize"), nil, "build", dir)
}

func execAtLocal(cmd string, input []byte, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.Command(cmd, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	if len(input) != 0 {
		command.Stdin = bytes.NewReader(input)
	}

	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func createNamespace(ns string) {
	stdout, stderr, err := kubectl("create", "namespace", ns)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred(), "stdout: %s, stderr: %s, err: %v", stdout, stderr, err)

	EventuallyWithOffset(1, func() error {
		stdout, stderr, err := kubectl("get", "sa", "default", "-n", ns)
		if err != nil {
			return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
		}
		return nil
	}).Should(Succeed())
}

func isDeploymentReady(name, namespace string, replicas int) error {
	stdout, stderr, err := kubectl("get", "deployment", name, "-n", namespace, "-o", "json")
	if err != nil {
		return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	}

	d := new(appsv1.Deployment)
	err = json.Unmarshal(stdout, d)
	if err != nil {
		return err
	}

	if int(d.Status.AvailableReplicas) != replicas {
		return fmt.Errorf("AvailableReplicas is not %d: %d", replicas, int(d.Status.AvailableReplicas))
	}
	return nil
}

func isNotFoundFromStderr(stderr []byte) bool {
	return strings.Contains(string(stderr), "(NotFound)")
}

func fetchRunnerPods(namespace, runnerpool string) (*corev1.PodList, error) {
	selector := fmt.Sprintf("%s=%s", constants.AppInstanceLabelKey, runnerpool)
	stdout, stderr, err := kubectl("get", "pods", "-n", namespace, "-l", selector, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	}

	pods := new(corev1.PodList)
	err = json.Unmarshal(stdout, pods)
	if err != nil {
		return nil, fmt.Errorf("stdout: %s, err: %v", stdout, err)
	}
	return pods, nil
}

func getPodNames(pods *corev1.PodList) []string {
	l := make([]string, len(pods.Items))
	for i := range pods.Items {
		l[i] = pods.Items[i].Name
	}
	sort.Strings(l)
	return l
}

func isPodReady(po *corev1.Pod) bool {
	for i := range po.Status.Conditions {
		cond := &po.Status.Conditions[i]
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func getDeletionTime(po *corev1.Pod) (time.Time, error) {
	stdout, stderr, err := kubectl("exec", po.Name, "-n", po.Namespace,
		"--", "curl", "-s", fmt.Sprintf("localhost:%d/%s", constants.RunnerListenPort, constants.DeletionTimeEndpoint))
	if err != nil {
		return time.Time{}, fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	}
	dt := &client.DeletionTimePayload{}
	err = json.Unmarshal(stdout, dt)
	if err != nil {
		return time.Time{}, err
	}

	return dt.DeletionTime, nil
}

func putDeletionTime(po *corev1.Pod, tm time.Time) error {
	stdout, stderr, err := kubectl("exec", po.Name, "-n", po.Namespace,
		"--", "curl", "-s", "-XPUT", fmt.Sprintf("localhost:%d/%s", constants.RunnerListenPort, constants.DeletionTimeEndpoint),
		"-H", "Content-Type: application/json",
		"-d", fmt.Sprintf("{\"deletion_time\":\"%s\"}", tm.Format(time.RFC3339)))
	if err != nil {
		return fmt.Errorf("stdout: %s, stderr: %s, err: %v", stdout, stderr, err)
	}
	return nil
}

func findPodToBeDeleted(pods *corev1.PodList) (string, time.Time) {
	for i := range pods.Items {
		po := &pods.Items[i]
		if !isPodReady(po) {
			continue
		}
		tm, err := getDeletionTime(po)
		if err != nil || tm.IsZero() {
			continue
		}
		return po.Namespace + "/" + po.Name, tm
	}
	return "", time.Time{}
}

func getRecretedPods(before, after *corev1.PodList) ([]string, []string) {
	delPodNames := make([]string, 0)
	addPodNames := make([]string, 0)

	beforeMap := make(map[string]bool)
	for i := range before.Items {
		beforeMap[before.Items[i].Name] = true
	}

	afterMap := make(map[string]bool)
	for i := range after.Items {
		afterMap[after.Items[i].Name] = true
	}

	for i := range before.Items {
		if !afterMap[before.Items[i].Name] {
			delPodNames = append(delPodNames, before.Items[i].Name)
		}
	}

	for i := range after.Items {
		if _, ok := beforeMap[after.Items[i].Name]; !ok {
			addPodNames = append(addPodNames, after.Items[i].Name)
		}
	}
	return delPodNames, addPodNames
}

func equalNumRecreatedPods(before, after *corev1.PodList, numRecreated int) error {
	if len(before.Items) != len(after.Items) {
		return fmt.Errorf(
			"length mismatch: expected %#v actual %#v",
			getPodNames(before),
			getPodNames(after),
		)
	}
	delPodNames, addPodNames := getRecretedPods(before, after)
	if len(delPodNames) != numRecreated || len(addPodNames) != numRecreated {
		return fmt.Errorf(
			"%d pod should be recreated: before %#v after %#v",
			numRecreated,
			getPodNames(before),
			getPodNames(after),
		)
	}
	return nil
}

func fetchOnlineRunnerNames(label string) ([]string, error) {
	return fetchRunnerNames(label, "online")
}

func fetchAllRunnerNames(label string) ([]string, error) {
	return fetchRunnerNames(label, "")
}

func fetchRunnerNames(label, status string) ([]string, error) {
	runners, res, err := githubClient.Actions.ListRunners(context.Background(), orgName, repoName, &github.ListOptions{Page: 0, PerPage: 100})
	if err != nil {
		return nil, err
	}
	if res.NextPage != 0 {
		panic("more than 100 runners exist: please delete them manually before running a test")
	}

	runnerNames := []string{}
OUTER:
	for _, r := range runners.Runners {
		if r.GetName() == "" || (status != "" && r.GetStatus() != status) {
			continue
		}

		for _, l := range r.Labels {
			if l.GetName() == label {
				runnerNames = append(runnerNames, *r.Name)
				continue OUTER
			}
		}
	}
	sort.Strings(runnerNames)

	return runnerNames, nil
}

func compareExistingRunners(label string, podNames []string) error {
	runnerNames, err := fetchOnlineRunnerNames(label)
	if err != nil {
		return err
	}
	if len(runnerNames) != len(podNames) || !cmp.Equal(runnerNames, podNames) {
		return fmt.Errorf("%d runners should exist: pods %#v runners %#v", len(podNames), podNames, runnerNames)
	}
	return nil
}

func gitSafe(args ...string) {
	var stdout, stderr bytes.Buffer
	command := exec.Command("git", args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Dir = testRepoWorkDir

	err := command.Run()
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred(), "stdout: %s, stderr: %s, err: %v", stdout.String(), stderr.String(), err)
}

func pushWorkflowFile(filename, namespace, runnerPoolName string) {
	workflowFile := filepath.Join(".github", "workflows", "testjob.yaml")

	var buf bytes.Buffer
	tpl := template.Must(template.ParseFiles(filepath.Join("./workflows", filename)))
	tpl.Execute(&buf, map[string]string{
		"Namespace":  namespace,
		"RunnerPool": runnerPoolName,
	})
	err := ioutil.WriteFile(filepath.Join(testRepoWorkDir, workflowFile), buf.Bytes(), 0644)
	Expect(err).ShouldNot(HaveOccurred())

	gitSafe("add", workflowFile)
	gitSafe("commit", "-m", "["+testID+"] "+filename)
	gitSafe("push", "--set-upstream", "origin", testBranch)
}
