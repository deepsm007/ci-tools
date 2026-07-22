package multi_stage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	coreapi "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	utilpointer "k8s.io/utils/pointer"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/ci-tools/pkg/api"
	"github.com/openshift/ci-tools/pkg/junit"
	base_steps "github.com/openshift/ci-tools/pkg/steps"
	"github.com/openshift/ci-tools/pkg/util"
)

func (s *multiStageTestStep) runSteps(
	ctx context.Context,
	phase string,
	steps []api.LiteralTestStep,
	env []coreapi.EnvVar,
	secretVolumes []coreapi.Volume,
	secretVolumeMounts []coreapi.VolumeMount,
	interruptCtx context.Context,
) error {
	start := time.Now()
	logrus.Infof("Running multi-stage phase %s", phase)
	pods, bestEffortSteps, err := s.generatePods(steps, env, secretVolumes, secretVolumeMounts, &generatePodOptions{
		enableSecretsStoreCSIDriver: s.enableSecretsStoreCSIDriver,
	})
	if err != nil {
		s.flags |= hasPrevErrs
		return err
	}
	var errs []error
	defer func() {
		if len(errs) != 0 {
			s.flags |= hasPrevErrs
		}
	}()
	if phase == "post" {
		err = s.runPostPods(ctx, interruptCtx, pods, steps, bestEffortSteps)
	} else {
		err = s.runPods(ctx, pods, bestEffortSteps)
	}
	if err != nil {
		errs = append(errs, err)
	}
	select {
	case <-ctx.Done():
		logrus.Infof("cleanup: Deleting pods with label %s=%s", MultiStageTestLabel, s.name)
		if err := s.client.DeleteAllOf(base_steps.CleanupCtx, &coreapi.Pod{}, ctrlruntimeclient.InNamespace(s.jobSpec.Namespace()), ctrlruntimeclient.MatchingLabels{MultiStageTestLabel: s.name}); err != nil && !kerrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete pods with label %s=%s: %w", MultiStageTestLabel, s.name, err))
		}
		errs = append(errs, fmt.Errorf("cancelled"))
	default:
		break
	}

	err = utilerrors.NewAggregate(errs)
	finished := time.Now()
	duration := finished.Sub(start)
	testCase := &junit.TestCase{
		Name:      fmt.Sprintf("Run multi-stage test %s phase", phase),
		Duration:  duration.Seconds(),
		SystemOut: fmt.Sprintf("The collected steps of multi-stage phase %s.", phase),
	}
	verb := "succeeded"
	if err != nil {
		verb = "failed"
		testCase.FailureOutput = &junit.FailureOutput{
			Output: err.Error(),
		}
	}
	s.subTests = append(s.subTests, testCase)
	logrus.Infof("Step phase %s %s after %s.", phase, verb, duration.Truncate(time.Second))

	return err
}

// isSoftPostStep reports whether a post step is non-critical (gather/artifacts).
// Critical post steps (e.g. deprovision) are everything else.
func isSoftPostStep(step api.LiteralTestStep) bool {
	return (step.BestEffort != nil && *step.BestEffort) ||
		(step.OptionalOnSuccess != nil && *step.OptionalOnSuccess)
}

func softPostStepNames(testName string, steps []api.LiteralTestStep) sets.Set[string] {
	names := sets.New[string]()
	for _, step := range steps {
		if isSoftPostStep(step) {
			names.Insert(fmt.Sprintf("%s-%s", testName, step.As))
		}
	}
	return names
}

func filterHardPostPods(pods []coreapi.Pod, softNames sets.Set[string]) []coreapi.Pod {
	var hard []coreapi.Pod
	for _, pod := range pods {
		if !softNames.Has(pod.Name) {
			hard = append(hard, pod)
		}
	}
	return hard
}

// runPostPods runs post steps in configured order. Soft steps (best_effort /
// optional_on_success) stay interruptible and still record junit pass/fail.
// Critical steps always use postCtx (Background) so timeout / new-push interrupt
// cannot cancel deprovision. On interrupt, soft steps are cancelled/skipped and
// remaining critical steps start immediately in the background.
func (s *multiStageTestStep) runPostPods(
	postCtx, interruptCtx context.Context,
	pods []coreapi.Pod,
	steps []api.LiteralTestStep,
	bestEffortSteps sets.Set[string],
) error {
	softNames := softPostStepNames(s.name, steps)

	hardErrCh := make(chan error, 1)
	var hardOnce sync.Once
	startHardFrom := func(from int) {
		hardOnce.Do(func() {
			go func() {
				hard := filterHardPostPods(pods[from:], softNames)
				if len(hard) == 0 {
					hardErrCh <- nil
					return
				}
				logrus.Infof("Running %d critical post step(s)", len(hard))
				hardErrCh <- s.runPods(postCtx, hard, bestEffortSteps)
			}()
		})
	}

	if interruptCtx != nil && interruptCtx.Err() != nil {
		logrus.Info("Interrupt before post: running critical post steps only")
		startHardFrom(0)
		return <-hardErrCh
	}

	var errs []error
	for i := 0; i < len(pods); i++ {
		if interruptCtx != nil && interruptCtx.Err() != nil {
			for _, pod := range pods[i:] {
				if softNames.Has(pod.Name) {
					logrus.Infof("Skipping non-critical post step %s due to interrupt", pod.Name)
				}
			}
			logrus.Info("Interrupt during post: running remaining critical post steps in background")
			startHardFrom(i)
			errs = append(errs, <-hardErrCh)
			return utilerrors.NewAggregate(errs)
		}

		pod := pods[i]
		isSoft := softNames.Has(pod.Name)
		podCtx := postCtx
		flags := util.WaitForPodFlag(0)
		var cancel context.CancelFunc
		done := make(chan struct{})
		if isSoft && interruptCtx != nil {
			podCtx, cancel = context.WithCancel(postCtx)
			go func(p coreapi.Pod, from int) {
				select {
				case <-interruptCtx.Done():
					logrus.Infof("Interrupt: cancelling non-critical post step %s", p.Name)
					cancel()
					if err := s.client.Delete(context.Background(), &p); err != nil && !kerrors.IsNotFound(err) {
						logrus.WithError(err).Warnf("failed to delete non-critical post pod %s", p.Name)
					}
					logrus.Info("Interrupt during post: starting critical post steps in background")
					startHardFrom(from)
				case <-done:
				}
			}(pod, i)
			flags = util.Interruptible
		}

		err := s.runPod(podCtx, &pod, base_steps.NewTestCaseNotifier(util.NopNotifier), flags)
		close(done)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			continue
		}
		if isSoft && interruptCtx != nil && interruptCtx.Err() != nil {
			logrus.Infof("Non-critical post step %s ended after interrupt (result recorded)", pod.Name)
			continue
		}
		if bestEffortSteps != nil && bestEffortSteps.Has(pod.Name) {
			logrus.Infof("Pod %s is running in best-effort mode, ignoring the failure...", pod.Name)
			continue
		}
		errs = append(errs, err)
	}
	return utilerrors.NewAggregate(errs)
}

func (s *multiStageTestStep) runPods(ctx context.Context, pods []coreapi.Pod, bestEffortSteps sets.Set[string]) error {
	var errs []error
	for _, pod := range pods {
		err := s.runPod(ctx, &pod, base_steps.NewTestCaseNotifier(util.NopNotifier), util.WaitForPodFlag(0))
		if err == nil {
			continue
		}
		if bestEffortSteps != nil && bestEffortSteps.Has(pod.Name) {
			logrus.Infof("Pod %s is running in best-effort mode, ignoring the failure...", pod.Name)
			continue
		}
		errs = append(errs, err)
		if s.flags&shortCircuit != 0 {
			break
		}
	}
	return utilerrors.NewAggregate(errs)
}

func (s *multiStageTestStep) runObservers(ctx, textCtx context.Context, pods []coreapi.Pod, done chan<- struct{}) {
	wg := sync.WaitGroup{}
	wg.Add(len(pods))
	errs := make(chan error, len(pods))
	for _, pod := range pods {
		go func(p coreapi.Pod) {
			<-ctx.Done()
			logrus.Infof("Signalling observer pod %q to terminate...", p.Name)
			if err := s.client.Delete(context.Background(), &p); err != nil {
				logrus.WithError(err).Warn("failed to trigger observer to stop")
			}
		}(pod)
		go func(p coreapi.Pod) {
			err := s.runPod(textCtx, &p, base_steps.NewTestCaseNotifier(util.NopNotifier), util.Interruptible)
			if ctx.Err() == nil {
				// when the observer is cancelled, we get an error here that we need to ignore, as it's not an error
				// for the Pod to be deleted when it's cancelled, it's just expected
				errs <- err
			} else {
				logrus.Debugf("ignoring observer error after cancellation: %v", err)
			}
			wg.Done()
		}(pod)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			logrus.WithError(err).Warn("observer failed")
		}
	}
	done <- struct{}{}
}

func (s *multiStageTestStep) runPod(ctx context.Context, pod *coreapi.Pod, notifier *base_steps.TestCaseNotifier, flags util.WaitForPodFlag) error {
	start := time.Now()
	logrus.Infof("Running step %s.", pod.Name)
	client := s.client.WithNewLoggingClient()

	client.MetricsAgent().StoreMachinesSnapshot(pod)

	if _, err := util.CreateOrRestartPod(ctx, client, pod); err != nil {
		return fmt.Errorf("failed to create or restart %s pod: %w", pod.Name, err)
	}
	newPod, err := util.WaitForPodCompletion(ctx, client, pod.Namespace, pod.Name, notifier, flags)
	if newPod != nil {
		pod = newPod
	}

	// If we got an error and the Pod is still pending (failed to schedule or failed to start all containers),
	// delete it to prevent it from recovering and potentially executing later, potentially simultaneously
	// with pods we run later (for example, we do not want to leave a Pending unschedulable cluster-testing
	// step Pod in the cluster, because it may eventually get scheduled and start testing the cluster that
	// is already being torn down).
	if err != nil && pod.Status.Phase == coreapi.PodPending {
		client.MetricsAgent().StorePodLifecycleMetrics(pod.Name, pod.Namespace, coreapi.PodFailed)
		client.MetricsAgent().StoreMachinesSnapshot(pod)
		logrus.Infof("Deleting pod %s that failed to start", pod.Name)
		deleteCtx, cancel := context.WithTimeout(base_steps.CleanupCtx, 30*time.Second)
		defer cancel()
		if delErr := client.Delete(deleteCtx, pod); delErr != nil && !kerrors.IsNotFound(delErr) {
			logrus.WithError(delErr).Warnf("Failed to delete pending pod %s", pod.Name)
		}
	}

	finished := time.Now()
	duration := finished.Sub(start)
	verb := "succeeded"
	if err != nil {
		verb = "failed"
	}
	logrus.Infof("Step %s %s after %s.", pod.Name, verb, duration.Truncate(time.Second))
	s.subLock.Lock()
	s.subSteps = append(s.subSteps, api.CIOperatorStepDetailInfo{
		StepName:    pod.Name,
		Description: fmt.Sprintf("Run pod %s", pod.Name),
		StartedAt:   &start,
		FinishedAt:  &finished,
		Duration:    &duration,
		Failed:      utilpointer.Bool(err != nil),
		Manifests:   client.Objects(),
	})
	s.subTests = append(s.subTests, notifier.SubTests(fmt.Sprintf("%s - %s ", s.Description(), pod.Name))...)
	s.subLock.Unlock()
	if err != nil {
		linksText := strings.Builder{}
		linksText.WriteString(fmt.Sprintf("Link to step on registry info site: https://steps.ci.openshift.org/reference/%s", strings.TrimPrefix(pod.Name, s.name+"-")))
		linksText.WriteString(fmt.Sprintf("\nLink to job on registry info site: https://steps.ci.openshift.org/job?org=%s&repo=%s&branch=%s&test=%s", s.config.Metadata.Org, s.config.Metadata.Repo, s.config.Metadata.Branch, s.name))
		if s.config.Metadata.Variant != "" {
			linksText.WriteString(fmt.Sprintf("&variant=%s", s.config.Metadata.Variant))
		}
		status := "failed"
		if pod.Status.Phase == coreapi.PodFailed && pod.Status.Reason == "DeadlineExceeded" {
			status = "exceeded the configured timeout"
			if pod.Spec.ActiveDeadlineSeconds != nil {
				status = fmt.Sprintf("%s activeDeadlineSeconds=%d", status, *pod.Spec.ActiveDeadlineSeconds)
			}
		}
		return fmt.Errorf("%q pod %q %s: %w\n%s", s.name, pod.Name, status, err, linksText.String())
	}
	return nil
}
