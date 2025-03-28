package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PagerDuty/go-pagerduty"
	jiraapi "github.com/andygrunwald/go-jira"
	"github.com/blang/semver"
	"github.com/sirupsen/logrus"
	"github.com/slack-go/slack"

	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/prow/pkg/config/secret"
	"sigs.k8s.io/prow/pkg/flagutil"
	prowflagutil "sigs.k8s.io/prow/pkg/flagutil"
	jirautil "sigs.k8s.io/prow/pkg/jira"
	"sigs.k8s.io/prow/pkg/logrusutil"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/ci-tools/pkg/api"
	"github.com/openshift/ci-tools/pkg/jira"
	"github.com/openshift/ci-tools/pkg/pagerdutyutil"
)

type options struct {
	logLevel string

	jiraOptions       prowflagutil.JiraOptions
	kubernetesOptions prowflagutil.KubernetesOptions
	pagerDutyOptions  pagerdutyutil.Options

	slackTokenPath string
	weekStart      bool

	enableBuild02UpgradeNotification bool
}

func (o *options) Validate() error {
	_, err := logrus.ParseLevel(o.logLevel)
	if err != nil {
		return fmt.Errorf("invalid --log-level: %w", err)
	}

	if o.slackTokenPath == "" {
		return fmt.Errorf("--slack-token-path is required")
	}

	for _, group := range []flagutil.OptionGroup{&o.jiraOptions, &o.pagerDutyOptions, &o.kubernetesOptions} {
		if err := group.Validate(false); err != nil {
			return err
		}
	}

	return nil
}

func gatherOptions(fs *flag.FlagSet, args ...string) options {
	o := options{kubernetesOptions: prowflagutil.KubernetesOptions{NOInClusterConfigDefault: true}}
	fs.StringVar(&o.logLevel, "log-level", "info", "Level at which to log output.")

	for _, group := range []flagutil.OptionGroup{&o.jiraOptions, &o.pagerDutyOptions, &o.kubernetesOptions} {
		group.AddFlags(fs)
	}

	fs.StringVar(&o.slackTokenPath, "slack-token-path", "", "Path to the file containing the Slack token to use.")
	fs.BoolVar(&o.weekStart, "week-start", false, "If set to true run in 'Monday' mode: performing, additional, Monday only activities")
	fs.BoolVar(&o.enableBuild02UpgradeNotification, "enable-build02-upgrade-notification", false, "If set to true send notification when build02 needs an upgrade")

	if err := fs.Parse(args); err != nil {
		logrus.WithError(err).Fatal("Could not parse args.")
	}
	return o
}

func addSchemes() error {
	if err := configv1.AddToScheme(scheme.Scheme); err != nil {
		return fmt.Errorf("failed to add configv1 to scheme: %w", err)
	}
	return nil
}

func main() {
	logrusutil.ComponentInit()

	o := gatherOptions(flag.NewFlagSet(os.Args[0], flag.ExitOnError), os.Args[1:]...)
	if err := o.Validate(); err != nil {
		logrus.WithError(err).Fatal("Invalid options")
	}
	level, _ := logrus.ParseLevel(o.logLevel)
	logrus.SetLevel(level)

	if err := secret.Add(o.slackTokenPath); err != nil {
		logrus.WithError(err).Fatal("Error starting secrets agent.")
	}

	slackClient := slack.New(string(secret.GetSecret(o.slackTokenPath)))
	pagerDutyClient, err := o.pagerDutyOptions.Client()
	if err != nil {
		logrus.WithError(err).Fatal("Could not initialize PagerDuty client.")
	}
	userIdsByRole, err := users(pagerDutyClient, slackClient)
	if err != nil {
		msg := "Could not get rotating roles from PagerDuty."
		if len(userIdsByRole) == 0 {
			logrus.WithError(err).Fatal(msg)
		} else {
			logrus.WithError(err).Error(msg)
		}
	}
	prowJiraClient, err := o.jiraOptions.Client()
	if err != nil {
		logrus.WithError(err).Fatal("Could not initialize Jira client.")
	}
	jiraClient := prowJiraClient.JiraClient()

	if err := sendTeamDigest(userIdsByRole, jiraClient, slackClient); err != nil {
		logrus.WithError(err).Fatal("Could not post team digest to Slack.")
	}

	if err := ensureGroupMembership(slackClient, userIdsByRole); err != nil {
		logrus.WithError(err).Fatal("Could not ensure Slack group membership.")
	}

	if err := assignAndSendIntakeDigest(slackClient, jiraClient, userIdsByRole[roleIntake]); err != nil {
		logrus.WithError(err).Fatal("Could not post @dptp-intake digest to Slack.")
	}

	if o.weekStart {
		if err := sendNextWeeksRoleDigest(pagerDutyClient, slackClient); err != nil {
			logrus.WithError(err).Fatal("Could not post next week's role digest to Slack.")
		}
		if err := notifyTriageOfHandover(slackClient, userIdsByRole[roleTriagePrimary].slackId); err != nil {
			logrus.WithError(err).Fatal("Could not notify triage engineer of handover doc via Slack.")
		}
	}

	if err := addSchemes(); err != nil {
		logrus.WithError(err).Fatal("failed to set up scheme")
	}
	kubeConfigs, err := o.kubernetesOptions.LoadClusterConfigs()
	if err != nil {
		logrus.WithError(err).Fatal("could not load kube configs")
	}

	clients := map[api.Cluster]ctrlruntimeclient.Reader{}
	for _, cluster := range []api.Cluster{api.ClusterBuild01, api.ClusterBuild02} {
		clusterName := string(cluster)
		config, ok := kubeConfigs[clusterName]
		if !ok {
			logrus.WithField("context", clusterName).Fatal("failed to find context in kube configs")
		}
		client, err := ctrlruntimeclient.New(&config, ctrlruntimeclient.Options{})
		if err != nil {
			logrus.WithField("clusterName", clusterName).WithError(err).Fatal("could not get client for kube config")
		}
		clients[cluster] = client
	}

	if o.enableBuild02UpgradeNotification {
		versionInfo, err := upgradeBuild02(context.TODO(), clients[api.ClusterBuild01], clients[api.ClusterBuild02])
		if err != nil {
			logrus.WithError(err).Fatal("could not determine if build02 needs to upgraded")
		}
		if versionInfo != nil {
			logrus.WithField("toVersion", versionInfo.version).Info("Posting @dptp-triage about upgrading build02 to Slack")
			if err := sendTriageBuild02Upgrade(slackClient, versionInfo.version, versionInfo.stableDuration); err != nil {
				logrus.WithError(err).Fatal("Could not post @dptp-triage about upgrading build02 to Slack.")
			}
		}
	} else {
		logrus.WithField("enableBuild02UpgradeNotification", o.enableBuild02UpgradeNotification).
			Info("Skipped checking if build02 needs an upgrade")
	}
}

const (
	primaryOnCallQuery                = "DPTP Primary On-Call"
	helpdeskQuery                     = "DPTP Help Desk"
	intakeQuery                       = "DPTP Intake"
	roleTriagePrimary                 = "@dptp-triage Primary"
	roleHelpdesk                      = "@dptp-helpdesk"
	roleIntake                        = "@dptp-intake"
	jiraUnassignedAssigneeDisplayName = "<Unassigned>"
	jiraUnassignedAssigneeAvatarUrl   = "https://issues.redhat.com/secure/useravatar?size=mm&avatarId=10283"
)

func sendTeamDigest(userIdsByRole map[string]user, jiraClient *jiraapi.Client, slackClient *slack.Client) error {
	blocks := getPagerDutyBlocks(userIdsByRole)

	if approvalBlocks, err := getIssuesNeedingApproval(jiraClient); err != nil {
		return fmt.Errorf("could not get issues needing approval: %w", err)
	} else {
		blocks = append(blocks, approvalBlocks...)
	}

	return postBlocks(slackClient, blocks)
}

func getPagerDutyBlocks(userIdsByRole map[string]user) []slack.Block {
	var fields []*slack.TextBlockObject
	for _, role := range []string{roleTriagePrimary, roleHelpdesk, roleIntake} {
		fields = append(fields, &slack.TextBlockObject{
			Type: slack.PlainTextType,
			Text: role,
		}, &slack.TextBlockObject{
			Type: slack.MarkdownType,
			Text: fmt.Sprintf("<@%s>", userIdsByRole[role].slackId),
		})
	}

	blocks := []slack.Block{
		&slack.HeaderBlock{
			Type: slack.MBTHeader,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "Today's Rotating Positions",
			},
		},
		&slack.SectionBlock{
			Type:   slack.MBTSection,
			Fields: fields,
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.MarkdownType,
				Text: "Role manuals for: <https://docs.google.com/document/d/1x2C0CISvCxr7IWz6dCHj7lYqxo6GG7CpxohinxllUnE|triage>, <https://docs.google.com/document/d/1cgNYGgyXQsi9YpHNPbxE7lVDff-74uVCzAd4o1xcwGo|help-desk>, and <https://docs.google.com/document/d/1Wmy9F8-SBEG4Cd_he9aiZUS8c66DUE0XmRTmu04uW6U|intake>.",
			},
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.MarkdownType,
				Text: "Team definitions for: <https://docs.google.com/document/d/1pvTfPovr1zGmt-CKTQEfJ2Y6UYanbGO8-3RvujE-rpw|ready>, <https://docs.google.com/document/d/1f2zJHg9evsrY2BArfmhuhavFbdghPD4jzW1pxRUL35o|done>.",
			},
		},
	}

	return blocks
}

type user struct {
	slackId string
	email   string
}

func users(client *pagerduty.Client, slackClient *slack.Client) (map[string]user, error) {
	now := time.Now()
	userIdsByRole, errors := usersOnCallAtTime(client, slackClient, now.Year(), now.Month(), now.Day())
	return userIdsByRole, kerrors.NewAggregate(errors)
}

func usersOnCallAtTime(client *pagerduty.Client, slackClient *slack.Client, year int, month time.Month, day int) (map[string]user, []error) {
	var errors []error
	userIdsByRole := map[string]user{}

	for _, item := range []struct {
		role  string
		query string
	}{
		{
			role:  roleTriagePrimary,
			query: primaryOnCallQuery,
		},
		{
			role:  roleHelpdesk,
			query: helpdeskQuery,
		},
		{
			role:  roleIntake,
			query: intakeQuery,
		},
	} {
		// 7 am UTC is when our PD day begins, and US on-call ends at 10pm UTC. Query 8 am - 9 pm for safe results
		dayStart := time.Date(year, month, day, 8, 0, 1, 0, time.UTC)
		dayEnd := dayStart.Add(13 * time.Hour).Add(-2 * time.Second)
		pagerDutyUser, err := userOnCallDuring(client, item.query, dayStart, dayEnd)
		if err != nil {
			errors = append(errors, fmt.Errorf("could not get PagerDuty user for %s: %w", item.role, err))
			continue
		}
		slackUser, err := slackClient.GetUserByEmail(pagerDutyUser.Email)
		if err != nil {
			errors = append(errors, fmt.Errorf("could not get slack user for %s: %w", pagerDutyUser.Name, err))
			continue
		}
		userIdsByRole[item.role] = user{slackId: slackUser.ID, email: pagerDutyUser.Email}
	}
	return userIdsByRole, errors
}

func userOnCallDuring(client *pagerduty.Client, query string, since, until time.Time) (*pagerduty.User, error) {
	scheduleResponse, err := client.ListSchedules(pagerduty.ListSchedulesOptions{Query: query})
	if err != nil {
		return nil, fmt.Errorf("could not query PagerDuty for the %s on-call schedule: %w", query, err)
	}
	if len(scheduleResponse.Schedules) != 1 {
		return nil, fmt.Errorf("did not get exactly one schedule when querying PagerDuty for the '%s' on-call schedule: %v", query, scheduleResponse.Schedules)
	}
	schedule := scheduleResponse.Schedules[0]

	users, err := client.ListOnCallUsers(schedule.ID, pagerduty.ListOnCallUsersOptions{
		Since: since.String(),
		Until: until.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("could not query PagerDuty for the %s on-call: %w", query, err)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("did not get any users when querying PagerDuty for the '%s' on-call: %v", query, users)
	} else if len(users) == 1 {
		return &users[0], nil
	}

	// more than 1 user means there must be an override, determine who the override is associated with
	overrides, err := client.ListOverrides(schedule.ID, pagerduty.ListOverridesOptions{
		Since: since.String(),
		Until: until.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("could not query PagerDuty for the '%s' overrides: %w", query, err)
	}
	if len(overrides.Overrides) != 1 {
		return nil, fmt.Errorf("did not get exactly one override when querying PagerDuty for the '%s' overrides: %v", query, overrides)
	}
	override := overrides.Overrides[0]

	user, err := client.GetUser(override.User.ID, pagerduty.GetUserOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get User: %s associated with the override: %v", override.User.ID, override)
	}
	return user, nil
}

func sendNextWeeksRoleDigest(client *pagerduty.Client, slackClient *slack.Client) error {
	var errors []error
	// Use one week from now at noon UTC to ensure that PD roles have begun
	nextWeek := time.Now().Add(7 * 24 * time.Hour)
	userIdsByRole, errs := usersOnCallAtTime(client, slackClient, nextWeek.Year(), nextWeek.Month(), nextWeek.Day())
	if len(errs) > 0 {
		errors = append(errors, errs...)
		msg := "Could not get rotating roles from PagerDuty."
		err := kerrors.NewAggregate(errors)
		if len(userIdsByRole) == 0 {
			logrus.WithError(err).Fatal(msg)
		} else {
			logrus.WithError(err).Error(msg)
		}
	}

	// Invert to group all roles for each userId as a user can be in multiple roles
	rolesByUserId := make(map[string][]string)
	for role, u := range userIdsByRole {
		userId := u.slackId
		if roles, ok := rolesByUserId[userId]; ok {
			rolesByUserId[userId] = append(roles, role)
		} else {
			rolesByUserId[userId] = []string{role}
		}
	}

	for userId, roles := range rolesByUserId {
		message := []slack.Block{
			&slack.HeaderBlock{
				Type: slack.MBTHeader,
				Text: &slack.TextBlockObject{
					Type: slack.PlainTextType,
					Text: "Next Week's Role",
				},
			},
			&slack.SectionBlock{
				Type: slack.MBTSection,
				Text: &slack.TextBlockObject{
					Type: slack.PlainTextType,
					Text: "Next week, you will be in the following roles:",
				},
			},
		}

		for _, role := range roles {
			message = append(message, &slack.ContextBlock{
				Type: slack.MBTContext,
				ContextElements: slack.ContextElements{
					Elements: []slack.MixedElement{
						&slack.TextBlockObject{
							Type: slack.PlainTextType,
							Text: role,
						},
					},
				},
			})
		}

		responseChannel, responseTimestamp, err := slackClient.PostMessage(
			userId,
			slack.MsgOptionText("Next week's role.", false),
			slack.MsgOptionBlocks(message...))
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to message userId: %s about next weeks role: %w", userId, err))
		} else {
			logrus.Infof("Posted next weeks role digest in channel %s at %s", responseChannel, responseTimestamp)
		}
	}

	return kerrors.NewAggregate(errors)
}

func getIssuesNeedingApproval(jiraClient *jiraapi.Client) ([]slack.Block, error) {
	issues, response, err := jiraClient.Issue.Search(fmt.Sprintf(`project=%s AND status=Review AND issuetype!=Sub-task`, jira.ProjectDPTP), nil)
	if err := jirautil.HandleJiraError(response, err); err != nil {
		return nil, fmt.Errorf("could not query for Jira issues: %w", err)
	}

	if len(issues) == 0 {
		return nil, nil
	}

	blocks := []slack.Block{
		&slack.HeaderBlock{
			Type: slack.MBTHeader,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "Cards Awaiting Acceptance",
			},
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "The following issues are ready for acceptance on the DPTP board:",
			},
		},
	}
	idByUser := map[string]slack.Block{}
	blocksByUser := map[string][]slack.Block{}
	for _, issue := range issues {
		assigneeDisplayName := jiraUnassignedAssigneeDisplayName
		assigneeAvatarUrl := jiraUnassignedAssigneeAvatarUrl
		if issue.Fields.Assignee != nil {
			assigneeDisplayName = issue.Fields.Assignee.DisplayName
			assigneeAvatarUrl = issue.Fields.Assignee.AvatarUrls.Four8X48
		}
		if _, recorded := idByUser[assigneeDisplayName]; !recorded {
			idByUser[assigneeDisplayName] = &slack.ContextBlock{
				Type: slack.MBTContext,
				ContextElements: slack.ContextElements{
					Elements: []slack.MixedElement{
						&slack.ImageBlockElement{
							Type:     slack.METImage,
							ImageURL: assigneeAvatarUrl,
							AltText:  assigneeDisplayName,
						},
						&slack.TextBlockObject{
							Type: slack.MarkdownType,
							Text: assigneeDisplayName,
						},
					},
				},
			}
		}
		blocksByUser[assigneeDisplayName] = append(blocksByUser[assigneeDisplayName], blockForIssue(issue))
	}

	for user, id := range idByUser {
		blocks = append(blocks, id)
		blocks = append(blocks, blocksByUser[user]...)
		blocks = append(blocks, &slack.DividerBlock{
			Type: slack.MBTDivider,
		})
	}
	return blocks, nil
}

const (
	dptpTeamChannel       = "team-dp-testplatform"
	dptpBuildFarmsChannel = "alerts-testplatform-build-farms"

	privateChannelType = "private_channel"
	publicChannelType  = "public_channel"
)

func channelID(slackClient *slack.Client, channel, t string) (string, error) {
	var channelID, cursor string
	for {
		conversations, nextCursor, err := slackClient.GetConversations(&slack.GetConversationsParameters{Cursor: cursor, Types: []string{t}})
		if err != nil {
			return "", fmt.Errorf("could not query Slack for channel ID: %w", err)
		}
		for _, conversation := range conversations {
			if conversation.Name == channel {
				channelID = conversation.ID
				break
			}
		}
		if channelID != "" || nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	if channelID == "" {
		return "", fmt.Errorf("could not find Slack channel %s", channel)
	}
	return channelID, nil
}

func postBlocks(slackClient *slack.Client, blocks []slack.Block) error {
	channelID, err := channelID(slackClient, dptpTeamChannel, privateChannelType)
	if err != nil {
		return fmt.Errorf("failed to get channel ID for %s: %w", dptpTeamChannel, err)
	}
	responseChannel, responseTimestamp, err := slackClient.PostMessage(channelID, slack.MsgOptionText("Jira card digest.", false), slack.MsgOptionBlocks(blocks...))
	if err != nil {
		return fmt.Errorf("failed to post to channel: %w", err)
	}

	logrus.Infof("Posted team digest in channel %s at %s", responseChannel, responseTimestamp)
	return nil
}

func assignAndSendIntakeDigest(slackClient *slack.Client, jiraClient *jiraapi.Client, user user) error {
	opts := jiraapi.SearchOptions{Fields: []string{"*navigable", "comment"}}
	issues, response, err := jiraClient.Issue.Search(fmt.Sprintf(`project=%s AND (labels is EMPTY OR NOT (labels=ready OR labels=no-intake)) AND created >= -30d AND status = "To Do" AND issuetype != Sub-task AND assignee is EMPTY`, jira.ProjectDPTP), &opts)
	if err := jirautil.HandleJiraError(response, err); err != nil {
		return fmt.Errorf("could not query for Jira issues: %w", err)
	}

	if len(issues) == 0 {
		logrus.Debug("No issues have been found for intake")
		return nil
	}

	blocks := []slack.Block{
		&slack.HeaderBlock{
			Type: slack.MBTHeader,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "Cards Awaiting Intake",
			},
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "The following issues need to be reviewed as part of the intake process:",
			},
		},
	}

	jiraUsers, userResponse, userErr := jiraClient.User.Find("", jiraapi.WithUsername(user.email))
	if err := jirautil.HandleJiraError(userResponse, userErr); err != nil {
		return fmt.Errorf("could not find jira user: %w", err)
	}
	if len(jiraUsers) != 1 {
		return fmt.Errorf("could not find single jira user for intake role, expected 1 got %d", len(jiraUsers))
	}
	for _, issue := range issues {
		_, err = jiraClient.Issue.UpdateAssignee(issue.ID, &jiraUsers[0])
		if err != nil {
			return fmt.Errorf("could not update assignee on issue(%s): %w", issue.ID, err)
		}
		blocks = append(blocks, blockForIssue(issue))
	}
	responseChannel, responseTimestamp, err := slackClient.PostMessage(user.slackId, slack.MsgOptionText("Jira card digest.", false), slack.MsgOptionBlocks(blocks...))
	if err != nil {
		return fmt.Errorf("failed to message @dptp-intake: %w", err)
	}

	logrus.Infof("Posted intake digest in channel %s at %s", responseChannel, responseTimestamp)
	return nil
}

type versionInfo struct {
	stable          bool
	stableDuration  string
	version         string
	state           configv1.UpdateState
	semanticVersion semver.Version
}

// newVersionInfo checks if the current version is stable enough.
// A version is stable iff Z-stream (or Y-stream) upgrade has been completed for 1 day (1 week).
// Z-stream upgrade: the current version is upgraded from the same minor version e.g., 4.8.23 <- 4.8.18
// Y-stream upgrade: the current version is upgraded from a smaller minor version e.g., 4.9.6 <- 4.8.18
func newVersionInfo(status configv1.ClusterVersionStatus) (*versionInfo, error) {
	if len(status.History) == 0 {
		return nil, fmt.Errorf("failed to get history of ClusterVersion version")
	}
	current := status.History[0]
	ret := &versionInfo{
		version: current.Version,
		state:   current.State,
		// soak a day after a Z-stream upgrade
		stable:         current.State == configv1.CompletedUpdate && current.CompletionTime != nil && time.Since(current.CompletionTime.Time) > 24*time.Hour,
		stableDuration: "1 day",
	}
	cv, err := semver.Make(current.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to determine semantic version: %s", current.Version)
	}
	ret.semanticVersion = cv
	if ret.stable && len(status.History) > 1 {
		previous := status.History[1]
		pv, err := semver.Make(previous.Version)
		if err != nil {
			return nil, fmt.Errorf("failed to determine semantic version: %s", previous.Version)
		}
		if cv.Minor > pv.Minor {
			// soak a week after a Y-stream upgrade
			ret.stable = time.Since(current.CompletionTime.Time) > 7*24*time.Hour
			ret.stableDuration = "7 days"
		}
	}
	return ret, nil
}

func clusterVersion(ctx context.Context, clusterName string, Client ctrlruntimeclient.Reader) (*versionInfo, error) {
	cv := &configv1.ClusterVersion{}
	if err := Client.Get(ctx, ctrlruntimeclient.ObjectKey{Name: "version"}, cv); err != nil {
		return nil, fmt.Errorf("failed to get ClusterVersion version on %s: %w", clusterName, err)
	}
	return newVersionInfo(cv.Status)
}

func upgradeBuild02(ctx context.Context, build01Client, build02Client ctrlruntimeclient.Reader) (*versionInfo, error) {
	build01VI, err := clusterVersion(ctx, "build01", build01Client)
	if err != nil {
		return nil, err
	}
	if !build01VI.stable {
		logrus.WithField("build01Version", build01VI.version).Info("The version on build01 has not been stable enough and hence no need to upgrade build02")
		return nil, nil
	}

	build02VI, err := clusterVersion(ctx, "build02", build02Client)
	if err != nil {
		return nil, err
	}
	if build02VI.state != configv1.CompletedUpdate {
		logrus.WithField("state", build02VI.state).Info("The previous upgrade of build02 has not been completed")
		return nil, nil
	}
	if build02VI.semanticVersion.GE(build01VI.semanticVersion) {
		logrus.WithField("build01VI.version", build01VI.version).WithField("build02VI.version", build02VI.version).Info("no need to upgrade build02")
		return nil, nil
	}
	return build01VI, nil
}

func sendTriageBuild02Upgrade(slackClient *slack.Client, version, stableDuration string) error {
	blocks := []slack.Block{
		&slack.HeaderBlock{
			Type: slack.MBTHeader,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "Upgrade Build02",
			},
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.MarkdownType,
				// Ideally, we could just run the upgrade command and notify triage via slack
				// In reality we still need some manual checks before upgrading build02
				Text: fmt.Sprintf("@%s `build01` has been stable with Version %s for %s. Please upgrade `build02` if it is healthy: `oc --as system:admin --context build02 adm upgrade --to=%s`",
					userGroupTriage, version, stableDuration, version),
			},
		},
	}

	channelID, err := channelID(slackClient, dptpBuildFarmsChannel, publicChannelType)
	if err != nil {
		return fmt.Errorf("failed for get channel ID for %s", dptpBuildFarmsChannel)
	}
	responseChannel, responseTimestamp, err := slackClient.PostMessage(channelID, slack.MsgOptionBlocks(blocks...))
	if err != nil {
		return fmt.Errorf("failed to message @dptp-triage: %w", err)
	}

	logrus.Infof("Posted message to triage in channel %s at %s", responseChannel, responseTimestamp)
	return nil
}

const dateFormat = "Mon, 02 Jan 2006"

func blockForIssue(issue jiraapi.Issue) slack.Block {
	// we really don't want these things to line wrap, so truncate the summary
	cutoff := 85
	summary := issue.Fields.Summary
	if len(summary) > cutoff {
		summary = summary[0:cutoff-3] + "..."
	}
	created := time.Time(issue.Fields.Created).Format(dateFormat)
	updated := time.Time(issue.Fields.Updated).Format(dateFormat)
	return &slack.ContextBlock{
		Type: slack.MBTContext,
		ContextElements: slack.ContextElements{
			Elements: []slack.MixedElement{
				&slack.TextBlockObject{
					Type: slack.MarkdownType,
					Text: fmt.Sprintf("<https://issues.redhat.com/browse/%s|*%s*>: %s \n", issue.Key, issue.Key, summary),
				},
				&slack.TextBlockObject{
					Type: slack.PlainTextType,
					Text: fmt.Sprintf("Created on: %s  Last updated: %s", created, updated),
				},
			},
		},
	}
}

const (
	userGroupTriage   = "dptp-triage"
	userGroupHelpdesk = "dptp-helpdesk"
)

func ensureGroupMembership(client *slack.Client, userIdsByRole map[string]user) error {
	groups, err := client.GetUserGroups(slack.GetUserGroupsOptionIncludeUsers(true))
	if err != nil {
		return fmt.Errorf("could not query Slack for groups: %w", err)
	}
	groupsByHandle := map[string]slack.UserGroup{}
	for i := range groups {
		groupsByHandle[groups[i].Handle] = groups[i]
	}
	for role, handle := range map[string]string{
		roleTriagePrimary: userGroupTriage,
		roleHelpdesk:      userGroupHelpdesk,
	} {
		group, found := groupsByHandle[handle]
		if !found {
			return fmt.Errorf("could not find user group %s", handle)
		}

		if expected, actual := sets.New[string](userIdsByRole[role].slackId), sets.New[string](group.Users...); !expected.Equal(actual) {
			if _, err := client.UpdateUserGroupMembers(group.ID, strings.Join(sets.List(expected), ",")); err != nil {
				return fmt.Errorf("failed to update group %s: %w", handle, err)
			}
		}
	}
	return nil
}

const handoverDocLocation = "https://docs.google.com/document/d/1l2ewqieLYjIUA7nsJiPoD4vscsyxgcGXWlTgjqcTzzk"

func notifyTriageOfHandover(client *slack.Client, triageId string) error {
	blocks := []slack.Block{
		&slack.HeaderBlock{
			Type: slack.MBTHeader,
			Text: &slack.TextBlockObject{
				Type: slack.PlainTextType,
				Text: "Triage Handover",
			},
		},
		&slack.SectionBlock{
			Type: slack.MBTSection,
			Text: &slack.TextBlockObject{
				Type: slack.MarkdownType,
				Text: fmt.Sprintf("Please review the <%s|*Triage Handover Document*> to view any ongoing incidents. Remember to add any relevant information to it during your time in the Triage role.", handoverDocLocation),
			},
		},
	}

	responseChannel, responseTimestamp, err := client.PostMessage(triageId, slack.MsgOptionText("Triage Handover", false), slack.MsgOptionBlocks(blocks...))
	if err != nil {
		return fmt.Errorf("failed to message @dptp-triage: %w", err)
	}

	logrus.Infof("Posted Triage Handover reminder in channel %s at %s", responseChannel, responseTimestamp)
	return nil
}
