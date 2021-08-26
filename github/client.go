package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/bradleyfalzon/ghinstallation"
	"github.com/google/go-github/v33/github"
)

const statusOnline = "online"

type Runner struct {
	ID     int64
	Name   string
	Online bool
	Busy   bool
	Labels []string
}

func convert(ghRunner *github.Runner) *Runner {
	var labels []string
	for _, l := range ghRunner.Labels {
		labels = append(labels, l.GetName())
	}
	return &Runner{
		Name:   ghRunner.GetName(),
		ID:     ghRunner.GetID(),
		Online: ghRunner.GetStatus() == statusOnline,
		Busy:   ghRunner.GetBusy(),
		Labels: labels,
	}
}

func (r *Runner) hasLabels(labels []string) bool {
	actualLabelMap := map[string]struct{}{}
	for _, l := range r.Labels {
		actualLabelMap[l] = struct{}{}
	}
	for _, required := range labels {
		if _, ok := actualLabelMap[required]; !ok {
			return false
		}
	}
	return true
}

// Client generates token for GitHub Action selfhosted runner
type Client interface {
	GetOrganizationName() string
	CreateRegistrationToken(context.Context, string) (*github.RegistrationToken, error)
	ListRunners(context.Context, string, []string) ([]*Runner, error)
	RemoveRunner(context.Context, string, int64) error
}

// clientImpl is GitHub clientImpl wrapper
type clientImpl struct {
	client           *github.Client
	organizationName string
}

// NewClient creates GitHub Actions Client
func NewClient(
	appID int64,
	appInstallationID int64,
	appPrivateKeyPath string,
	organizationName string,
) (Client, error) {
	rt, err := ghinstallation.NewKeyFromFile(
		http.DefaultTransport,
		appID,
		appInstallationID,
		appPrivateKeyPath,
	)
	if err != nil {
		return nil, err
	}
	return &clientImpl{
		client:           github.NewClient(&http.Client{Transport: rt}),
		organizationName: organizationName,
	}, nil
}

// GetOrganizationName returns organizationName.
func (c *clientImpl) GetOrganizationName() string {
	return c.organizationName
}

// CreateRegistrationToken creates an Actions token to register self-hosted runner to the organization.
func (c *clientImpl) CreateRegistrationToken(ctx context.Context, repositoryName string) (*github.RegistrationToken, error) {
	token, res, err := c.client.Actions.CreateRegistrationToken(
		ctx,
		c.organizationName,
		repositoryName,
	)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("invalid status code %d", res.StatusCode)
	}

	return token, nil
}

// ListRunners lists registered self-hosted runners for the organization.
func (c *clientImpl) ListRunners(ctx context.Context, repositoryName string, labels []string) ([]*Runner, error) {
	var runners []*Runner

	opts := github.ListOptions{PerPage: 100}
	for {
		list, res, err := c.client.Actions.ListRunners(
			ctx,
			c.organizationName,
			repositoryName,
			&opts,
		)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("invalid status code %d", res.StatusCode)
		}

		for _, ghRunner := range list.Runners {
			r := convert(ghRunner)
			if !r.hasLabels(labels) {
				continue
			}
			runners = append(runners, r)
		}
		if res.NextPage == 0 {
			break
		}

		opts.Page = res.NextPage
		time.Sleep(500 * time.Microsecond)
	}
	return runners, nil
}

// RemoveRunner deletes an Actions runner of the organization.
func (c *clientImpl) RemoveRunner(ctx context.Context, repositoryName string, runnerID int64) error {
	res, err := c.client.Actions.RemoveRunner(
		ctx,
		c.organizationName,
		repositoryName,
		runnerID,
	)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("invalid status code %d", res.StatusCode)
	}
	return nil
}

// FakeClient is a fake client
type FakeClient struct {
	organizationName  string
	runners           map[string][]*Runner
	expiredAtDuration time.Duration
}

// NewFakeClient creates GitHub Actions Client.
func NewFakeClient(organizationName string) *FakeClient {
	return &FakeClient{
		organizationName:  organizationName,
		expiredAtDuration: 1 * time.Hour,
	}
}

// GetOrganizationName returns organizationName.
func (c *FakeClient) GetOrganizationName() string {
	return c.organizationName
}

// CreateRegistrationToken returns dummy token.
func (c *FakeClient) CreateRegistrationToken(ctx context.Context, repositoryName string) (*github.RegistrationToken, error) {
	fakeToken := "faketoken"
	return &github.RegistrationToken{
		Token: &fakeToken,
		ExpiresAt: &github.Timestamp{
			Time: time.Now().Add(c.expiredAtDuration),
		},
	}, nil
}

// ListRunners returns dummy list.
func (c *FakeClient) ListRunners(ctx context.Context, repositoryName string, labels []string) ([]*Runner, error) {
	ret := []*Runner{}
	runners := c.runners[repositoryName]
	for _, r := range runners {
		if r.hasLabels(labels) {
			ret = append(ret, r)
		}
	}
	return ret, nil
}

// RemoveRunner does not delete anything and returns success.
func (c *FakeClient) RemoveRunner(ctx context.Context, repositoryName string, runnerID int64) error {
	// skip existance and nil check below because this is mock
	runners := c.runners[repositoryName]
	for i, v := range runners {
		if v.ID == runnerID {
			c.runners[repositoryName] = append(runners[:i], runners[i+1:]...)
			return nil
		}
	}
	return errors.New("not exist")
}

// SetRunners sets runners for multiple repositories
func (c *FakeClient) SetRunners(runners map[string][]*Runner) {
	c.runners = runners
}
