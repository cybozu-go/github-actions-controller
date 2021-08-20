package controllers

import (
	"context"
	"time"

	constants "github.com/cybozu-go/meows"
	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/github"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func NewSecretWatcher(client client.Client, interval time.Duration, githubClient github.Client) SecretWatcher {
	return SecretWatcher{
		client:       client,
		interval:     interval,
		githubClient: githubClient,
	}
}

type SecretWatcher struct {
	client       client.Client
	interval     time.Duration
	githubClient github.Client
}

// Start implements Runnable.Start
func (w SecretWatcher) Start(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	logger := log.FromContext(ctx).WithName("SecretWatcher")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			logger.Info("start secret watcher")
			err := w.secretExpired(ctx)
			if err != nil {
				return err
			}
		}
	}
}

func (w SecretWatcher) secretExpired(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("SecretWatcher")
	rps := meowsv1alpha1.RunnerPoolList{}
	err := w.client.List(ctx, &rps)
	if err != nil {
		return err
	}
	for _, rp := range rps.Items {
		s := new(corev1.Secret)
		err := w.client.Get(ctx, types.NamespacedName{Name: rp.GetRunnerSecretName(), Namespace: rp.Namespace}, s)
		if err != nil {
			logger.Error(err, "failed to get secret")
			continue
		}
		expiresAtStr, ok := s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey]
		if ok {
			expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
			if err != nil {
				logger.Error(err, "failed to parse expires-at annotation")
				continue
			}
			if expiresAt.Add(-5 * time.Minute).After(time.Now()) {
				logger.Info("skip update secret", "runnerpool_name", rp.Name, "expires_at", expiresAtStr)
				continue
			}
			logger.Info("update token as it has expired", "runnerpool_name", rp.Name, "expires_at", expiresAtStr)
		} else {
			logger.Info("create first token", "runnerpool_name", rp.Name)
		}
		op, err := ctrl.CreateOrUpdate(ctx, w.client, s, func() error {
			runnerToken, err := w.githubClient.CreateRegistrationToken(ctx, rp.Spec.RepositoryName)
			if err != nil {
				logger.Error(err, "failed to create actions registration token", "repository", rp.Spec.RepositoryName)
				return err
			}

			s.Annotations = mergeMap(s.Annotations, map[string]string{
				constants.RunnerSecretExpiresAtAnnotationKey: runnerToken.GetExpiresAt().Time.Format(time.RFC3339),
			})
			s.StringData = map[string]string{
				"runnertoken": runnerToken.GetToken(),
			}
			return nil
		})

		if err != nil {
			logger.Error(err, "failed to reconcile secret")
			return err
		}
		if op != controllerutil.OperationResultNone {
			logger.Info("reconciled runnerSecret", "operation", string(op))
		}
	}
	return nil
}
