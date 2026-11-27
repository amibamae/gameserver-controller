package main

import (
	"github.com/amibamae/gameserver-controller/internal/coordination"
)

func cmdRegister(c *cli, args []string) error {
	o := c.newOptions("register", true)
	var r coordination.Registration
	var caps string
	o.fs.StringVar(&r.Name, "name", "", "agent name (required)")
	o.fs.StringVar(&r.ID, "id", "", "agent ID to register or refresh (default: the agent with this session ID, else the next agent-N)")
	o.fs.StringVar(&r.SessionID, "session-id", "", "agent session ID")
	o.fs.StringVar(&r.SessionID, "sessionID", "", "alias for --session-id")
	o.fs.StringVar(&r.Version, "version", "", "agent version")
	o.fs.StringVar(&r.Role, "role", "", "agent role (default implementation)")
	o.fs.StringVar(&r.Worktree, "worktree", "", "the agent's worktree path")
	o.fs.StringVar(&caps, "capabilities", "", "comma-separated capabilities")
	if err := o.parse(args, required{"name", &r.Name}); err != nil {
		return err
	}
	if caps != "" {
		r.Capabilities = splitList(caps)
	}
	return o.apply(coordination.Register(r))
}

func cmdClaim(c *cli, args []string) error {
	o := c.newOptions("claim", true).withAgent()
	task := o.fs.String("task-id", "", "task to claim (required)")
	agent, err := o.parseWithAgent(args, required{"task-id", task})
	if err != nil {
		return err
	}
	return o.apply(coordination.Claim(agent, *task))
}

func cmdReserve(c *cli, args []string) error {
	o := c.newOptions("reserve", true).withAgent()
	o.allowArgs = true // a shell may expand a --files glob into several arguments
	files := o.fs.String("files", "", "comma-separated file paths or globs, relative to the repository root (required)")
	task := o.fs.String("for", "", "task the files are for (default: the task the agent holds)")
	agent, err := o.parseWithAgent(args, required{"files", files})
	if err != nil {
		return err
	}
	patterns := append(splitList(*files), o.fs.Args()...)
	return o.apply(coordination.Reserve(agent, *task, patterns))
}

func cmdUpdate(c *cli, args []string) error {
	o := c.newOptions("update", true).withAgent()
	task := o.fs.String("task-id", "", "claimed task (required)")
	progress := o.fs.String("progress", "", "progress note (required)")
	unblock := o.fs.Bool("unblock", false, "also clear the task's blocker")
	agent, err := o.parseWithAgent(args, required{"task-id", task}, required{"progress", progress})
	if err != nil {
		return err
	}
	return o.apply(coordination.Update(agent, *task, *progress, *unblock))
}

func cmdBlock(c *cli, args []string) error {
	o := c.newOptions("block", true).withAgent()
	task := o.fs.String("task-id", "", "claimed task (required)")
	reason := o.fs.String("reason", "", "what blocks it (required)")
	agent, err := o.parseWithAgent(args, required{"task-id", task}, required{"reason", reason})
	if err != nil {
		return err
	}
	return o.apply(coordination.Block(agent, *task, *reason))
}

func cmdHandoff(c *cli, args []string) error {
	o := c.newOptions("handoff", true).withAgent()
	task := o.fs.String("task-id", "", "claimed task (required)")
	to := o.fs.String("to-agent", "", "agent to hand the task to (required)")
	reason := o.fs.String("reason", "", "why (required)")
	agent, err := o.parseWithAgent(args, required{"task-id", task}, required{"to-agent", to}, required{"reason", reason})
	if err != nil {
		return err
	}
	return o.apply(coordination.Handoff(agent, *task, *to, *reason))
}

func cmdRelease(c *cli, args []string) error {
	o := c.newOptions("release", true).withAgent()
	task := o.fs.String("task-id", "", "task to release (required)")
	force := o.fs.Bool("force-reconcile", false, "release another agent's claim if it has expired")
	agent, err := o.parseWithAgent(args, required{"task-id", task})
	if err != nil {
		return err
	}
	return o.apply(coordination.Release(agent, *task, *force))
}

func cmdComplete(c *cli, args []string) error {
	o := c.newOptions("complete", true).withAgent()
	task := o.fs.String("task-id", "", "task to complete (required)")
	pr := o.fs.String("pr-number", "", "pull request that delivered it")
	agent, err := o.parseWithAgent(args, required{"task-id", task})
	if err != nil {
		return err
	}
	return o.apply(coordination.Complete(agent, *task, *pr))
}

// reconcileActor names reconcile in the audit log when no agent is given:
// reconcile is maintenance an operator may run without registering.
const reconcileActor = "operator"

func cmdReconcile(c *cli, args []string) error {
	o := c.newOptions("reconcile", true).withAgent()
	if err := o.parse(args); err != nil {
		return err
	}
	actor := o.agentOrEnv()
	if actor == "" {
		actor = reconcileActor
	}
	return o.apply(coordination.Reconcile(actor))
}
