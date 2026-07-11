package agents

import (
	"fmt"
	"iter"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

func init() {
	Register("repeater", func() (agent.Agent, error) {
		return agent.New(agent.Config{
			Name:        "repeater",
			Description: "An agent that repeats the user's message and counts turns.",
			Run:         runRepeater,
		})
	})
}

// runRepeater echoes the user's message back, prefaced with a turn count
// derived from the session event history. It demonstrates multi-turn context
// within a single invocation by reading prior events from the session.
func runRepeater(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
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

		turn := 1
		if sess := ctx.Session(); sess != nil {
			events := sess.Events()
			for i := events.Len() - 1; i >= 0; i-- {
				ev := events.At(i)
				if ev != nil && ev.Author == "repeater" {
					turn++
					break
				}
			}
		}

		reply := fmt.Sprintf("[turn %d] You said: %s", turn, strings.TrimSpace(userText))

		event := session.NewEvent(ctx, ctx.InvocationID())
		event.Author = ctx.Agent().Name()
		event.Content = &genai.Content{
			Role:  genai.RoleModel,
			Parts: []*genai.Part{{Text: reply}},
		}
		yield(event, nil)
	}
}
