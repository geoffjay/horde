// Package agents defines the ADK agents available to the horde system.
//
// All agents live in this top-level package and are spawned by the horde
// server as subprocesses. Each agent is built on top of the V2 ADK from
// Google (google.golang.org/adk/v2).
package agents

import (
	"iter"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// New constructs the greeter agent — a hello-world style custom ADK agent.
//
// It does not call an LLM; it simply echoes a greeting back to the user. It
// exists so the rest of the horde plumbing (server, subprocess spawning,
// TUI) has something to run.
func New() (agent.Agent, error) {
	return agent.New(agent.Config{
		Name:        "greeter",
		Description: "A hello-world agent that greets the user.",
		Run:         runGreeter,
	})
}

// runGreeter is the agent's run function. It reads the user's input text and
// yields a single event containing a greeting that references that input.
func runGreeter(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		userText := ""
		if uc := ctx.UserContent(); uc != nil {
			for _, part := range uc.Parts {
				if part.Text != "" {
					userText = part.Text
					break
				}
			}
		}

		reply := "Hello from horde!"
		if userText != "" {
			reply = "Hello from horde! You said: " + strings.TrimSpace(userText)
		}

		event := session.NewEvent(ctx, ctx.InvocationID())
		event.Author = ctx.Agent().Name()
		event.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Text: reply}},
		}
		yield(event, nil)
	}
}
