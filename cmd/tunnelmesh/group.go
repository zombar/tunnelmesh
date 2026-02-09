package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func newGroupCmd() *cobra.Command {
	groupCmd := &cobra.Command{
		Use:   "group",
		Short: "Manage groups",
		Long: `Manage TunnelMesh groups.

Groups allow you to assign permissions to multiple peers at once.
Built-in groups: everyone, all_admin_users

Examples:
  # List all groups
  tunnelmesh group list

  # Create a new group
  tunnelmesh group create developers --description "Development team"

  # Add a member to a group
  tunnelmesh group add-member developers peer123

  # Remove a member from a group
  tunnelmesh group remove-member developers peer123`,
	}

	// List subcommand
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all groups",
		RunE:  runGroupList,
	}
	groupCmd.AddCommand(listCmd)

	// Create subcommand
	createCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new group",
		Args:  cobra.ExactArgs(1),
		RunE:  runGroupCreate,
	}
	createCmd.Flags().StringP("description", "d", "", "Group description")
	groupCmd.AddCommand(createCmd)

	// Delete subcommand
	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a group",
		Args:  cobra.ExactArgs(1),
		RunE:  runGroupDelete,
	}
	groupCmd.AddCommand(deleteCmd)

	// Members subcommand
	membersCmd := &cobra.Command{
		Use:   "members <name>",
		Short: "List members of a group",
		Args:  cobra.ExactArgs(1),
		RunE:  runGroupMembers,
	}
	groupCmd.AddCommand(membersCmd)

	// Add-member subcommand
	addMemberCmd := &cobra.Command{
		Use:   "add-member <group> <peer-id>",
		Short: "Add a member to a group",
		Args:  cobra.ExactArgs(2),
		RunE:  runGroupAddMember,
	}
	groupCmd.AddCommand(addMemberCmd)

	// Remove-member subcommand
	removeMemberCmd := &cobra.Command{
		Use:   "remove-member <group> <peer-id>",
		Short: "Remove a member from a group",
		Args:  cobra.ExactArgs(2),
		RunE:  runGroupRemoveMember,
	}
	groupCmd.AddCommand(removeMemberCmd)

	// Grant subcommand
	grantCmd := &cobra.Command{
		Use:   "grant <group> <role>",
		Short: "Grant a role to a group",
		Long: `Grant a role to a group, optionally scoped to a bucket.

Examples:
  # Grant admin role to a group
  tunnelmesh group grant admins admin

  # Grant bucket-read role scoped to a specific bucket
  tunnelmesh group grant readers bucket-read --bucket fs+data`,
		Args: cobra.ExactArgs(2),
		RunE: runGroupGrant,
	}
	grantCmd.Flags().StringP("bucket", "b", "", "Bucket scope for the role")
	groupCmd.AddCommand(grantCmd)

	return groupCmd
}

// nolint:revive // args required by cobra.Command RunE signature
func runGroupList(_ *cobra.Command, args []string) error {
	adminURL := getAdminURL()

	resp, err := makeAdminRequest("GET", adminURL+"/api/groups", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to list groups: %s", string(body))
	}

	var groups []*auth.Group
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tMEMBERS\tBUILTIN\tDESCRIPTION")
	for _, g := range groups {
		builtin := ""
		if g.Builtin {
			builtin = "yes"
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", g.Name, len(g.Members), builtin, g.Description)
	}
	_ = w.Flush()

	return nil
}

func runGroupCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	description, _ := cmd.Flags().GetString("description")

	adminURL := getAdminURL()

	reqBody := map[string]string{
		"name":        name,
		"description": description,
	}
	body, _ := json.Marshal(reqBody)

	resp, err := makeAdminRequest("POST", adminURL+"/api/groups", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create group: %s", string(respBody))
	}

	fmt.Printf("Group '%s' created successfully\n", name)
	return nil
}

func runGroupDelete(cmd *cobra.Command, args []string) error {
	name := args[0]

	adminURL := getAdminURL()

	resp, err := makeAdminRequest("DELETE", adminURL+"/api/groups/"+name, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete group: %s", string(body))
	}

	fmt.Printf("Group '%s' deleted\n", name)
	return nil
}

func runGroupMembers(cmd *cobra.Command, args []string) error {
	name := args[0]

	adminURL := getAdminURL()

	resp, err := makeAdminRequest("GET", adminURL+"/api/groups/"+name+"/members", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to get members: %s", string(body))
	}

	var members []string
	if err := json.NewDecoder(resp.Body).Decode(&members); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if len(members) == 0 {
		fmt.Printf("Group '%s' has no members\n", name)
		return nil
	}

	fmt.Printf("Members of '%s':\n", name)
	for _, m := range members {
		fmt.Printf("  %s\n", m)
	}

	return nil
}

func runGroupAddMember(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	peerID := args[1]

	adminURL := getAdminURL()

	reqBody := map[string]string{"peer_id": peerID}
	body, _ := json.Marshal(reqBody)

	resp, err := makeAdminRequest("POST", adminURL+"/api/groups/"+groupName+"/members", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add member: %s", string(respBody))
	}

	fmt.Printf("Peer '%s' added to group '%s'\n", peerID, groupName)
	return nil
}

func runGroupRemoveMember(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	peerID := args[1]

	adminURL := getAdminURL()

	resp, err := makeAdminRequest("DELETE", adminURL+"/api/groups/"+groupName+"/members/"+peerID, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to remove member: %s", string(body))
	}

	fmt.Printf("Peer '%s' removed from group '%s'\n", peerID, groupName)
	return nil
}

func runGroupGrant(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	roleName := args[1]
	bucketScope, _ := cmd.Flags().GetString("bucket")

	adminURL := getAdminURL()

	reqBody := map[string]string{
		"role_name":    roleName,
		"bucket_scope": bucketScope,
	}
	body, _ := json.Marshal(reqBody)

	resp, err := makeAdminRequest("POST", adminURL+"/api/groups/"+groupName+"/bindings", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to grant role: %s", string(respBody))
	}

	if bucketScope != "" {
		fmt.Printf("Role '%s' granted to group '%s' on bucket '%s'\n", roleName, groupName, bucketScope)
	} else {
		fmt.Printf("Role '%s' granted to group '%s'\n", roleName, groupName)
	}
	return nil
}

// getAdminURL returns the URL of the coordinator admin interface.
// The admin interface is accessible at https://this.tunnelmesh/ from within the mesh.
func getAdminURL() string {
	// Admin is always at https://this.tunnelmesh (or this.tm for short)
	return "https://this.tunnelmesh"
}

// makeAdminRequest makes a request to the admin API.
func makeAdminRequest(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Use insecure client for mesh TLS (self-signed CA)
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	return client.Do(req)
}
