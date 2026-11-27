package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/keyadmin"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type scopeFlags []string

func (s *scopeFlags) String() string { return strings.Join(*s, ",") }
func (s *scopeFlags) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type localAdminAuthorizer struct{ isRoot bool }

func (a localAdminAuthorizer) AuthorizeIssue(context.Context, string, keyadmin.IssueRequest) error {
	if !a.isRoot {
		return errors.New("local administrator privileges are required")
	}
	return nil
}

func (a localAdminAuthorizer) AuthorizeRevoke(context.Context, string, string) error {
	if !a.isRoot {
		return errors.New("local administrator privileges are required")
	}
	return nil
}

type keyIssueOutput struct {
	ID        string          `json:"id"`
	Token     string          `json:"token"`
	ActorID   string          `json:"actor_id"`
	Audience  string          `json:"audience"`
	Scopes    []authkey.Scope `json:"scopes"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

const keyUsage = "usage: controller-setup keys {issue|revoke} [options]"

type keyOptions struct {
	databasePath, actorID, audience, expiresIn, keyID, confirmKeyID string
	scopes                                                          scopeFlags
}

func (opts *keyOptions) parse(operation string, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("controller-setup keys "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.databasePath, "database", "", "existing configured bbolt database")
	flags.StringVar(&opts.actorID, "actor", "", "actor identity assigned to the new key")
	flags.StringVar(&opts.audience, "audience", "", "exact controller audience for the key")
	flags.StringVar(&opts.expiresIn, "expires-in", "", "required key lifetime (1m through 90d)")
	flags.StringVar(&opts.keyID, "key-id", "", "key identifier to revoke")
	flags.StringVar(&opts.confirmKeyID, "confirm-key-id", "", "repeat the exact key identifier to confirm revocation")
	flags.Var(&opts.scopes, "scope", "exact permission:environment:resource scope; repeat for multiple scopes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(opts.databasePath) == "" {
		return errors.New("--database must name the existing controller database")
	}
	return nil
}

func (o keyOptions) issueRequest() (keyadmin.IssueRequest, error) {
	if o.keyID != "" || o.confirmKeyID != "" {
		return keyadmin.IssueRequest{}, errors.New("issue does not accept revocation flags")
	}
	if strings.TrimSpace(o.actorID) == "" || strings.TrimSpace(o.audience) == "" || o.expiresIn == "" || len(o.scopes) == 0 {
		return keyadmin.IssueRequest{}, errors.New("issue requires --actor, --audience, --expires-in, and at least one --scope")
	}
	lifetime, err := time.ParseDuration(o.expiresIn)
	if err != nil {
		return keyadmin.IssueRequest{}, errors.New("--expires-in must be a Go duration between 1m and 90d")
	}
	parsedScopes := make([]authkey.Scope, 0, len(o.scopes))
	for _, value := range o.scopes {
		parts := strings.Split(value, ":")
		if len(parts) != 3 {
			return keyadmin.IssueRequest{}, errors.New("each --scope must be permission:environment:resource")
		}
		parsedScopes = append(parsedScopes, authkey.Scope{Permission: parts[0], Environment: parts[1], Resource: parts[2]})
	}
	return keyadmin.IssueRequest{ActorID: o.actorID, Audience: o.audience, Scopes: parsedScopes, Lifetime: lifetime}, nil
}

func (o keyOptions) validateRevocation() error {
	if o.actorID != "" || o.audience != "" || o.expiresIn != "" || len(o.scopes) != 0 {
		return errors.New("revoke does not accept issuance flags")
	}
	if o.keyID == "" || o.confirmKeyID != o.keyID {
		return errors.New("revoke requires --key-id and --confirm-key-id with the exact same identifier")
	}
	return nil
}

func runKeyCommand(args []string, stdout, stderr io.Writer, isRoot bool, uid int) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, keyUsage)
		return errors.New("invalid command")
	}
	operation := args[0]
	var o keyOptions
	if err := o.parse(operation, args[1:], stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var req keyadmin.IssueRequest
	var err error
	switch operation {
	case "issue":
		req, err = o.issueRequest()
	case "revoke":
		err = o.validateRevocation()
	default:
		fmt.Fprintln(stderr, keyUsage)
		return errors.New("invalid command")
	}
	if err != nil {
		return err
	}
	if !isRoot {
		return errors.New("key administration requires local administrator privileges")
	}
	return o.execute(operation, req, stdout, isRoot, uid)
}

func (o keyOptions) execute(operation string, issueRequest keyadmin.IssueRequest, stdout io.Writer, isRoot bool, uid int) error {
	if err := requireExistingDatabase(o.databasePath); err != nil {
		return err
	}
	db, err := store.OpenBolt(filepath.Clean(o.databasePath))
	if err != nil {
		return err
	}
	defer db.Close()
	manager, err := authkey.NewManager(db)
	if err != nil {
		return err
	}
	service, err := keyadmin.NewService(manager, localAdminAuthorizer{isRoot: isRoot}, time.Now)
	if err != nil {
		return err
	}
	requesterID := "uid-" + strconv.Itoa(uid)
	if operation == "issue" {
		issued, err := service.Issue(context.Background(), requesterID, issueRequest)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(keyIssueOutput{ID: issued.ID, Token: issued.Secret, ActorID: issued.ActorID,
			Audience: issued.Audience, Scopes: issued.Scopes, CreatedAt: issued.CreatedAt, ExpiresAt: issued.ExpiresAt}); err != nil {
			return fmt.Errorf("could not write newly issued key %s; it may have been created, revoke it before retrying", issued.ID)
		}
		return nil
	}
	if err := service.Revoke(context.Background(), requesterID, o.keyID); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "API key revoked.")
	return nil
}

func requireExistingDatabase(path string) error {
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || directory.Mode()&os.ModeSymlink != 0 || !directory.IsDir() {
		return errors.New("controller database directory must be an existing directory, not a symlink")
	}
	if directory.Mode().Perm()&0o022 != 0 {
		return errors.New("controller database directory must not be writable by group or other users")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("controller database must already exist; check --database")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("controller database must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("controller database has group or world permissions; restrict it before managing keys")
	}
	return nil
}
