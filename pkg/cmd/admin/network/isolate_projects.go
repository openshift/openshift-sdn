package network

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	kcmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	kerrors "k8s.io/kubernetes/pkg/util/errors"

	"github.com/openshift/origin/pkg/cmd/util/clientcmd"
	"github.com/openshift/origin/pkg/sdn/registry/netnamespace/vnid"
)

const (
	IsolateProjectsNetworkCommandName = "isolate-projects"

	isolateProjectsNetworkLong = `
Isolate project network

Allows projects to isolate their network from other projects when using the %[1]s network plugin.`

	isolateProjectsNetworkExample = `	# Provide isolation for project p1
	$ %[1]s <p1>

	# Allow all projects with label name=top-secret to have their own isolated project network
	$ %[1]s --selector='name=top-secret'`
)

type IsolateOptions struct {
	Options *ProjectOptions
}

func NewCmdIsolateProjectsNetwork(commandName, fullName string, f *clientcmd.Factory, out io.Writer) *cobra.Command {
	opts := &ProjectOptions{}
	isolateOp := &IsolateOptions{Options: opts}

	cmd := &cobra.Command{
		Use:     commandName,
		Short:   "Isolate project network",
		Long:    fmt.Sprintf(isolateProjectsNetworkLong, ovsPluginName),
		Example: fmt.Sprintf(isolateProjectsNetworkExample, fullName),
		Run: func(c *cobra.Command, args []string) {
			if err := opts.Complete(f, c, args, out); err != nil {
				kcmdutil.CheckErr(err)
			}
			opts.CheckSelector = c.Flag("selector").Changed
			if err := opts.Validate(); err != nil {
				kcmdutil.CheckErr(kcmdutil.UsageError(c, err.Error()))
			}

			err := isolateOp.Run()
			kcmdutil.CheckErr(err)
		},
	}
	flags := cmd.Flags()

	// Common optional params
	flags.StringVar(&opts.Selector, "selector", "", "Label selector to filter projects. Either pass one/more projects as arguments or use this project selector")

	return cmd
}

func (i *IsolateOptions) Run() error {
	projects, err := i.Options.GetProjects()
	if err != nil {
		return err
	}

	netnsList, err := i.Options.GetNetNamespaces()
	if err != nil {
		return err
	}
	netIDNamespaceMap := make(map[string]uint, len(netnsList.Items))
	netIDCountMap := make(map[uint]uint, len(netnsList.Items))
	for _, netns := range netnsList.Items {
		netIDNamespaceMap[netns.ObjectMeta.Name] = *netns.NetID
		netIDCountMap[*netns.NetID] += 1
	}

	errList := []error{}
	for _, project := range projects {
		netID, exists := netIDNamespaceMap[project.ObjectMeta.Name]
		// Create new NetID in these cases
		// - NetNamespace doesn't exist
		// - Sharing network with other projects
		// - Global network namespace
		if !exists || (netIDCountMap[netID] > 1) || (netID == vnid.GlobalVNID) {
			err = i.Options.CreateNewNetID(project.ObjectMeta.Name)
			if err != nil {
				errList = append(errList, fmt.Errorf("Project %q can not be isolated, error: %v", project.ObjectMeta.Name, err))
			}
		}
	}
	return kerrors.NewAggregate(errList)
}
