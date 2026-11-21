package slack

import (
	"context"
	"encoding/json"
	"strings"

	slackgo "github.com/slack-go/slack"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

// HandleInteraction handles a block action or a modal submission. For a
// submission it returns the answer for Slack (nil closes the modal).
func (s *Service) HandleInteraction(ctx context.Context, cb slackgo.InteractionCallback) any {
	if s.api() == nil {
		return nil
	}
	team := cb.Team.ID
	if team == "" {
		team = s.installation().teamID
	}
	channel := cb.Channel.ID
	if channel == "" {
		channel = cb.Container.ChannelID
	}
	threadTS := cb.Container.ThreadTs
	if threadTS == "" {
		threadTS = cb.Message.ThreadTimestamp
	}
	r := reply{responseURL: cb.ResponseURL, channel: channel, threadTS: threadTS, user: cb.User.ID}
	if cb.Container.Type == "view" && cb.View.Type == slackgo.VTHomeTab {
		r.responseURL, r.channel = "", "" // nowhere to answer but a DM
	}
	uctx, u, ok, err := s.linkedUser(ctx, team, cb.User.ID)
	if err != nil {
		s.lg.Warn("slack user lookup", "err", err)
		return nil
	}
	switch cb.Type {
	case slackgo.InteractionTypeViewSubmission:
		if !ok {
			return nil
		}
		return s.submit(ctx, uctx, u.ID, cb)
	case slackgo.InteractionTypeBlockActions:
	default:
		return nil
	}
	for _, act := range cb.ActionCallback.BlockActions {
		switch act.ActionID {
		case "link", "open_web", "pick_default":
			continue // links open in the browser; the checkbox is read on pick
		}
		if !ok {
			if r.channel != "" || r.responseURL != "" {
				s.askToLink(ctx, r, team)
			} else {
				s.publishHome(ctx, team, cb.User.ID)
			}
			return nil
		}
		s.action(ctx, uctx, u.ID, team, cb, act, r)
	}
	return nil
}

func (s *Service) fail(ctx context.Context, r reply, what string, err error) {
	if r.channel == "" && r.responseURL == "" {
		if ch, derr := s.dmChannel(ctx, r.user); derr == nil {
			r.channel = ch
		}
	}
	s.say(ctx, r, ":warning: "+what+": "+escape(s.errorText(err))+".")
}

func (s *Service) action(ctx, uctx context.Context, userID, team string, cb slackgo.InteractionCallback, act *slackgo.BlockAction, r reply) {
	switch {
	case act.ActionID == "stop":
		if _, err := s.o.API.CancelSession(uctx, gen.CancelSessionRequestObject{SessionId: act.Value}); err != nil {
			s.fail(ctx, r, "Could not stop the turn", err)
		}
	case strings.HasPrefix(act.ActionID, "perm_"):
		var v permValue
		if json.Unmarshal([]byte(act.Value), &v) != nil {
			return
		}
		if _, err := s.o.API.AnswerPermission(uctx, gen.AnswerPermissionRequestObject{SessionId: v.Session, RequestId: v.Request, Body: &gen.AnswerPermissionJSONRequestBody{OptionId: v.Option}}); err != nil {
			s.fail(ctx, r, "Could not answer", err)
		}
	case act.ActionID == "model":
		view, err := s.modelView(uctx, act.Value, nil)
		if err != nil {
			s.fail(ctx, r, "Could not load the models", err)
			return
		}
		s.openView(ctx, cb.TriggerID, view)
	case act.ActionID == "model_select":
		var meta modelMeta
		_ = json.Unmarshal([]byte(cb.View.PrivateMetadata), &meta)
		sel := act.SelectedOption.Value
		view, err := s.modelView(uctx, meta.Session, &sel)
		if err != nil {
			return
		}
		if c := s.api(); c != nil {
			if _, err := c.UpdateViewContext(ctx, view, "", cb.View.Hash, cb.View.ID); err != nil {
				s.lg.Warn("update model view", "err", slackError(err))
			}
		}
	case act.ActionID == "more":
		what, sessionID, _ := strings.Cut(act.SelectedOption.Value, ":")
		res, err := s.o.API.GetSession(uctx, gen.GetSessionRequestObject{SessionId: sessionID})
		if err != nil {
			s.fail(ctx, r, "Could not open the thread", err)
			return
		}
		sess := gen.Session(res.(gen.GetSession200JSONResponse))
		switch what {
		case "rename":
			s.openView(ctx, cb.TriggerID, renameView(sess))
		case "delete":
			s.openView(ctx, cb.TriggerID, deleteView(sess))
		}
	case act.ActionID == "pick":
		s.onPick(ctx, uctx, userID, cb, act, r)
	case act.ActionID == "start_chat":
		res, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: act.Value})
		if err != nil {
			s.fail(ctx, r, "Could not start a thread", err)
			return
		}
		channel := r.channel
		if channel == "" {
			if channel, err = s.dmChannel(ctx, cb.User.ID); err != nil {
				return
			}
		}
		s.startChat(ctx, chatStart{team: team, channel: channel, slackUser: cb.User.ID, userID: userID, agent: gen.Agent(res.(gen.GetAgent200JSONResponse))})
	case act.ActionID == "use_here":
		if r.channel == "" {
			return
		}
		res, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: act.Value})
		if err != nil {
			s.fail(ctx, r, "Could not use the agent", err)
			return
		}
		a := gen.Agent(res.(gen.GetAgent200JSONResponse))
		if err := s.setDefault(ctx, team, r.channel, a.Id, userID); err != nil {
			s.fail(ctx, r, "Could not use the agent", err)
			return
		}
		s.say(ctx, r, "*"+escape(a.Name)+"* now answers new threads here.")
	case act.ActionID == "continue":
		channel := r.channel
		if channel == "" {
			var err error
			if channel, err = s.dmChannel(ctx, cb.User.ID); err != nil {
				return
			}
		}
		s.continueChat(ctx, team, channel, userID, act.Value, r)
	}
}

func (s *Service) openView(ctx context.Context, triggerID string, view slackgo.ModalViewRequest) {
	c := s.api()
	if c == nil || triggerID == "" {
		return
	}
	if _, err := c.OpenViewContext(ctx, triggerID, view); err != nil {
		s.lg.Warn("open view", "err", slackError(err))
	}
}

func (s *Service) onPick(ctx, uctx context.Context, userID string, cb slackgo.InteractionCallback, act *slackgo.BlockAction, r reply) {
	id := strings.TrimPrefix(act.BlockID, "pick:")
	s.mu.Lock()
	p := s.picks[id]
	if p != nil && p.slackUser == cb.User.ID {
		delete(s.picks, id)
	}
	s.mu.Unlock()
	if p == nil || p.slackUser != cb.User.ID {
		s.say(ctx, r, "That message is no longer waiting; send it again.")
		return
	}
	res, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: act.SelectedOption.Value})
	if err != nil {
		s.fail(ctx, r, "Could not use the agent", err)
		return
	}
	agent := gen.Agent(res.(gen.GetAgent200JSONResponse))
	if cb.BlockActionState != nil {
		if def := cb.BlockActionState.Values["pickdef:"+id]["pick_default"]; len(def.SelectedOptions) > 0 {
			if err := s.setDefault(ctx, p.team, p.channel, agent.Id, userID); err != nil {
				s.lg.Warn("set slack default agent", "err", err)
			}
		}
	}
	s.deleteOriginal(ctx, cb.ResponseURL)
	s.startChat(ctx, chatStart{team: p.team, channel: p.channel, threadTS: p.threadTS, slackUser: p.slackUser, userID: p.userID, agent: agent, text: p.text, msgTS: p.msgTS})
}

// submit handles a modal submission.
func (s *Service) submit(ctx, uctx context.Context, userID string, cb slackgo.InteractionCallback) any {
	var meta modelMeta
	_ = json.Unmarshal([]byte(cb.View.PrivateMetadata), &meta)
	values := map[string]map[string]slackgo.BlockAction{}
	if cb.View.State != nil {
		values = cb.View.State.Values
	}
	switch cb.View.CallbackID {
	case "model":
		choice := gen.ModelChoice{}
		if sel := values["model"]["model_select"].SelectedOption.Value; sel != "" && sel != defaultValue {
			choice.Model = sel
		}
		opts := map[string]string{}
		for blockID, acts := range values {
			id, ok := strings.CutPrefix(blockID, "opt:")
			if !ok {
				continue
			}
			for _, a := range acts {
				switch a.Type {
				case slackgo.ActionType(slackgo.OptTypeStatic):
					if v := a.SelectedOption.Value; v != "" && v != defaultValue {
						opts[id] = v
					}
				case slackgo.ActionType(slackgo.METCheckboxGroups):
					if len(a.SelectedOptions) > 0 {
						opts[id] = a.SelectedOptions[0].Value
					}
				}
			}
		}
		choice.Options = &opts
		if _, err := s.o.API.SetSessionModel(uctx, gen.SetSessionModelRequestObject{SessionId: meta.Session, Body: &choice}); err != nil {
			return slackgo.NewErrorsViewSubmissionResponse(map[string]string{"model": truncate(s.errorText(err), 150)})
		}
		s.goTracked(func() { s.refreshHeaders(s.ctx, meta.Session) })
	case "rename":
		title := strings.TrimSpace(values["title"]["title"].Value)
		if _, err := s.o.API.RenameSession(uctx, gen.RenameSessionRequestObject{SessionId: meta.Session, Body: &gen.RenameSessionJSONRequestBody{Title: title}}); err != nil {
			return slackgo.NewErrorsViewSubmissionResponse(map[string]string{"title": truncate(s.errorText(err), 150)})
		}
		s.goTracked(func() { s.refreshHeaders(s.ctx, meta.Session) })
	case "delete":
		rows, _ := s.o.Store.ListSlackThreadsOfSession(ctx, meta.Session)
		if _, err := s.o.API.DeleteSession(uctx, gen.DeleteSessionRequestObject{SessionId: meta.Session}); err != nil {
			v := slackgo.ModalViewRequest{
				Type: slackgo.VTModal, Title: plain("Delete thread"), Close: plain("Close"),
				Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{sectionBlock(":warning: The thread was not deleted: " + escape(s.errorText(err)) + ".")}},
			}
			return slackgo.NewUpdateViewSubmissionResponse(&v)
		}
		name := "someone"
		if u, err := s.o.Store.GetUser(ctx, userID); err == nil {
			name = u.Name
		}
		var keys []threadKey
		for _, row := range rows {
			keys = append(keys, threadKey{row.TeamID, row.ChannelID, row.ThreadTs})
		}
		s.forgetSession(meta.Session, keys)
		s.goTracked(func() {
			for _, row := range rows {
				s.markDeleted(s.ctx, row, name)
			}
		})
	}
	return nil
}

// markDeleted replaces a thread's header after its chat was deleted.
func (s *Service) markDeleted(ctx context.Context, row db.SlackThread, by string) {
	text := ":wastebasket: This thread was deleted by *" + escape(by) + "*. Mention me to start a new one."
	if row.HeaderTs != "" {
		_ = s.update(ctx, row.ChannelID, row.HeaderTs, "Thread deleted", []slackgo.Block{sectionBlock(text)})
		return
	}
	_, _ = s.post(ctx, row.ChannelID, row.ThreadTs, "Thread deleted", []slackgo.Block{contextBlock(text)})
}

// modelMeta is the private metadata of the modals.
type modelMeta struct {
	Session string `json:"s"`
}

const defaultValue = "default"

func meta(sessionID string) string {
	b, _ := json.Marshal(modelMeta{Session: sessionID})
	return string(b)
}

func renameView(sess gen.Session) slackgo.ModalViewRequest {
	in := slackgo.NewPlainTextInputBlockElement(plain("Title"), "title")
	in.InitialValue = sess.Title
	in.MaxLength = 200
	return slackgo.ModalViewRequest{
		Type: slackgo.VTModal, CallbackID: "rename", PrivateMetadata: meta(sess.Id),
		Title: plain("Rename thread"), Submit: plain("Rename"), Close: plain("Cancel"),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{slackgo.NewInputBlock("title", plain("Title"), nil, in)}},
	}
}

func deleteView(sess gen.Session) slackgo.ModalViewRequest {
	return slackgo.ModalViewRequest{
		Type: slackgo.VTModal, CallbackID: "delete", PrivateMetadata: meta(sess.Id),
		Title: plain("Delete thread"), Submit: plain("Delete"), Close: plain("Cancel"),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{
			slackgo.NewSectionBlock(mrkdwn("Delete *"+escape(truncate(sess.Title, 200))+"*? Its conversation is deleted for everyone, in the web UI too. This cannot be undone."), nil, nil, slackgo.SectionBlockOptionBlockID("confirm")),
		}},
	}
}

// modelView is the model modal of a session. selected is the model picked
// in the open modal (nil: the session's choice).
func (s *Service) modelView(uctx context.Context, sessionID string, selected *string) (slackgo.ModalViewRequest, error) {
	sres, err := s.o.API.GetSession(uctx, gen.GetSessionRequestObject{SessionId: sessionID})
	if err != nil {
		return slackgo.ModalViewRequest{}, err
	}
	sess := gen.Session(sres.(gen.GetSession200JSONResponse))
	ares, err := s.o.API.GetAgent(uctx, gen.GetAgentRequestObject{AgentId: sess.AgentId})
	if err != nil {
		return slackgo.ModalViewRequest{}, err
	}
	agent := gen.Agent(ares.(gen.GetAgent200JSONResponse))
	cat := s.catalogOf(uctx, agent.Id)
	view := slackgo.ModalViewRequest{
		Type: slackgo.VTModal, CallbackID: "model", PrivateMetadata: meta(sess.Id),
		Title: plain("Model"), Close: plain("Cancel"),
	}
	var blocks []slackgo.Block
	blocks = append(blocks, contextBlock("*"+escape(agent.Name)+"* · "+escape(truncate(sess.Title, 100))+" · applies from the next message"))
	own := gen.ModelChoice{}
	if sess.Model != nil {
		own = *sess.Model
	}
	ownOpts := map[string]string{}
	if own.Options != nil {
		ownOpts = *own.Options
	}
	current := own.Model
	if selected != nil {
		current = *selected
		if current == defaultValue {
			current = ""
		}
	}
	switch {
	case cat == nil:
		blocks = append(blocks, sectionBlock("This agent has no models to choose from."))
		view.Blocks = slackgo.Blocks{BlockSet: blocks}
		return view, nil
	case !cat.Switchable:
		blocks = append(blocks, sectionBlock("This harness picks its model when it starts; a thread cannot choose its own."))
		view.Blocks = slackgo.Blocks{BlockSet: blocks}
		return view, nil
	case agent.Role != gen.Owner && cat.Source != gen.Harness:
		blocks = append(blocks, sectionBlock("The agent's owner chooses the model until the harness has reported its models (after the agent's first thread). This thread uses "+escape(modelLabel(cat, sess, agent))+"."))
		view.Blocks = slackgo.Blocks{BlockSet: blocks}
		return view, nil
	}
	defLabel := "Agent default (" + choiceLabel(cat, agent.Model, agent.ModelOptions, "harness default") + ")"
	opts := []*slackgo.OptionBlockObject{slackgo.NewOptionBlockObject(defaultValue, plain(truncate(defLabel, 75)), nil)}
	initial := opts[0]
	listed := false
	for _, m := range cat.Models {
		if len(opts) == 100 {
			break
		}
		label := m.Name
		if m.Group != nil && *m.Group != "" {
			label = *m.Group + " · " + m.Name
		}
		o := slackgo.NewOptionBlockObject(m.Id, plain(truncate(label, 75)), nil)
		if m.Description != nil && *m.Description != "" {
			o.Description = plain(truncate(*m.Description, 75))
		}
		opts = append(opts, o)
		if m.Id == current {
			initial, listed = o, true
		}
	}
	if current != "" && !listed {
		o := slackgo.NewOptionBlockObject(current, plain(truncate(current+" (custom)", 75)), nil)
		opts = append(opts, o)
		initial = o
	}
	sel := slackgo.NewOptionsSelectBlockElement(slackgo.OptTypeStatic, plain("Model"), "model_select", opts...)
	sel.InitialOption = initial
	blocks = append(blocks, slackgo.NewInputBlock("model", plain("Model"), nil, sel).WithDispatchAction(true))
	// The model's own options, or while unknown those its harness reported
	// for other models.
	var options []gen.ModelOption
	for _, m := range cat.Models {
		if m.Id == current && m.Options != nil {
			options = *m.Options
		}
	}
	if options == nil && cat.ProvisionalOptions != nil {
		options = *cat.ProvisionalOptions
	}
	keep := selected == nil || current == own.Model
	for _, o := range options {
		blockID := "opt:" + o.Id
		switch o.Kind {
		case gen.Effort:
			vals := []*slackgo.OptionBlockObject{slackgo.NewOptionBlockObject(defaultValue, plain("Default"), nil)}
			init := vals[0]
			for _, v := range o.Values {
				ob := slackgo.NewOptionBlockObject(v.Value, plain(truncate(v.Name, 75)), nil)
				vals = append(vals, ob)
				if keep && ownOpts[o.Id] == v.Value {
					init = ob
				}
			}
			el := slackgo.NewOptionsSelectBlockElement(slackgo.OptTypeStatic, plain(o.Name), "value", vals...)
			el.InitialOption = init
			in := slackgo.NewInputBlock(blockID, plain(truncate(o.Name, 150)), nil, el)
			in.Optional = true
			blocks = append(blocks, in)
		case gen.Toggle:
			if len(o.Values) != 2 {
				continue
			}
			on := slackgo.NewOptionBlockObject(o.Values[1].Value, plain("On"), nil)
			el := slackgo.NewCheckboxGroupsBlockElement("value", on)
			if keep && ownOpts[o.Id] == o.Values[1].Value {
				el.InitialOptions = []*slackgo.OptionBlockObject{on}
			}
			in := slackgo.NewInputBlock(blockID, plain(truncate(o.Name, 150)), nil, el)
			in.Optional = true
			blocks = append(blocks, in)
		}
	}
	if cat.Source == gen.Fallback {
		blocks = append(blocks, contextBlock("The list is agenthold's until the harness reports its own (after the agent's first thread)."))
	}
	view.Submit = plain("Save")
	view.Blocks = slackgo.Blocks{BlockSet: blocks}
	return view, nil
}
