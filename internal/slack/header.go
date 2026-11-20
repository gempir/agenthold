package slack

import (
	"context"
	"sort"
	"strings"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

// catalogOf returns the agent's model catalog as the user sees it (nil if
// it has none).
func (s *Service) catalogOf(uctx context.Context, agentID string) *gen.ModelCatalog {
	res, err := s.o.API.ListModels(uctx, gen.ListModelsRequestObject{AgentId: agentID})
	if err != nil {
		return nil
	}
	c := gen.ModelCatalog(res.(gen.ListModels200JSONResponse))
	return &c
}

// allOptions are the options a catalog knows of, by id.
func allOptions(cat *gen.ModelCatalog) map[string]gen.ModelOption {
	out := map[string]gen.ModelOption{}
	if cat == nil {
		return out
	}
	for _, m := range cat.Models {
		if m.Options != nil {
			for _, o := range *m.Options {
				out[o.Id] = o
			}
		}
	}
	if cat.ProvisionalOptions != nil {
		for _, o := range *cat.ProvisionalOptions {
			if _, ok := out[o.Id]; !ok {
				out[o.Id] = o
			}
		}
	}
	return out
}

// choiceLabel names a model choice ("Opus 5.5 · High · Fast").
func choiceLabel(cat *gen.ModelCatalog, model string, options map[string]string, fallback string) string {
	name := fallback
	if model != "" {
		name = model
		if cat != nil {
			for _, m := range cat.Models {
				if m.Id == model {
					name = m.Name
				}
			}
		}
	}
	known := allOptions(cat)
	ids := make([]string, 0, len(options))
	for id := range options {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := []string{name}
	for _, kind := range []gen.ModelOptionKind{gen.Effort, gen.Toggle} {
		for _, id := range ids {
			o, ok := known[id]
			if !ok || o.Kind != kind {
				continue
			}
			v := options[id]
			switch kind {
			case gen.Effort:
				label := v
				for _, x := range o.Values {
					if x.Value == v {
						label = x.Name
					}
				}
				parts = append(parts, label)
			case gen.Toggle:
				if len(o.Values) == 2 && v == o.Values[1].Value {
					parts = append(parts, o.Name)
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

// modelLabel names the model a session uses.
func modelLabel(cat *gen.ModelCatalog, sess gen.Session, agent gen.Agent) string {
	if sess.Model != nil && (sess.Model.Model != "" || (sess.Model.Options != nil && len(*sess.Model.Options) > 0)) {
		opts := map[string]string{}
		if sess.Model.Options != nil {
			opts = *sess.Model.Options
		}
		model, fallback := sess.Model.Model, "Agent default"
		if model == "" && agent.Model != "" {
			model = agent.Model
		}
		return choiceLabel(cat, model, opts, fallback)
	}
	return "Agent default (" + choiceLabel(cat, agent.Model, agent.ModelOptions, "harness default") + ")"
}

// headerBlocks describe a thread's chat: agent, state, model, origin, and
// what can be done with it.
func (s *Service) headerBlocks(uctx context.Context, sess gen.Session, agent gen.Agent) ([]slackgo.Block, string) {
	cat := s.catalogOf(uctx, agent.Id)
	head := ":robot_face: *" + escape(agent.Name) + "*  " + stateEmoji(agent.State) + " " + string(agent.State)
	if sess.Title != "" && sess.Title != "New session" {
		head += "\n" + escape(truncate(sess.Title, 200))
	}
	ctxLines := []string{":brain: " + escape(modelLabel(cat, sess, agent))}
	if sess.Origin != nil {
		ctxLines = append(ctxLines, "Started by agent *"+escape(sess.Origin.AgentName)+"*")
	}
	ctxLines = append(ctxLines, "Chat of "+escape(sess.CreatedBy.Name)+" · replies in this thread go to the agent")
	more := slackgo.NewOverflowBlockElement("more",
		slackgo.NewOptionBlockObject("rename:"+sess.Id, plain("Rename"), nil),
		slackgo.NewOptionBlockObject("delete:"+sess.Id, plain("Delete chat"), nil),
	)
	blocks := []slackgo.Block{
		sectionBlock(head),
		contextBlock(strings.Join(ctxLines, "  ·  ")),
		slackgo.NewActionBlock("header",
			slackgo.NewButtonBlockElement("model", sess.Id, plain("Model")),
			linkButton("open_web", "Open in web", s.chatURL(agent.Id, sess.Id)),
			more,
		),
	}
	return blocks, agent.Name + ": " + sess.Title
}

// refreshHeader redraws a thread's header as the user who started it.
func (s *Service) refreshHeader(ctx context.Context, row db.SlackThread) {
	if row.HeaderTs == "" {
		return
	}
	uctx, err := s.userContext(ctx, row.CreatedBy)
	if err != nil {
		return
	}
	sres, err := s.o.API.GetSession(uctx, gen.GetSessionRequestObject{SessionId: row.SessionID})
	if err != nil {
		return
	}
	ares, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: row.AgentID})
	if err != nil {
		return
	}
	blocks, text := s.headerBlocks(uctx, gen.Session(sres.(gen.GetSession200JSONResponse)), gen.Agent(ares.(gen.GetAgent200JSONResponse)))
	_ = s.update(ctx, row.ChannelID, row.HeaderTs, text, blocks)
}

// refreshHeaders redraws the headers of every thread of a session.
func (s *Service) refreshHeaders(ctx context.Context, sessionID string) {
	rows, err := s.o.Store.ListSlackThreadsOfSession(ctx, sessionID)
	if err != nil {
		return
	}
	for _, r := range rows {
		s.refreshHeader(ctx, r)
	}
}
