package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonradoff/lofp/internal/gameworld"
)

// doEmoteWithScripts checks object and room scripts before falling back to processEmote.
// Priority: (1) item/room scripts when a target is named, (2) room bare-verb scripts, (3) processEmote.
// Used by emote verbs (KICK, GAZE, LISTEN, etc.) that can also trigger scripted interactions.
func (e *GameEngine) doEmoteWithScripts(ctx context.Context, player *Player, verb string, args []string) *CommandResult {
	room := e.rooms[player.RoomNumber]
	if len(args) > 0 {
		target := strings.ToLower(strings.Join(args, " "))
		if result := e.runVerbScriptsForTarget(ctx, player, room, verb, target); result != nil {
			return result
		}
	}
	// Check room-level bare-verb scripts (IFVERB/IFPREVERB VERB -1)
	if room != nil {
		sc := e.RunRoomVerbScripts(player, room, verb)
		if len(sc.DeferredSegments) > 0 {
			e.scheduleScriptSegments(player, sc.DeferredSegments)
		}
		if sc.Blocked || len(sc.Messages) > 0 || len(sc.RoomMsgs) > 0 {
			result := &CommandResult{Messages: sc.Messages, RoomBroadcast: sc.RoomMsgs, GMBroadcast: sc.GMMsgs}
			if sc.Blocked && len(result.Messages) == 0 {
				result.Messages = []string{"You can't do that."}
			}
			if !sc.Blocked {
				// Script ran but didn't block — also run the normal emote
				emResult := e.processEmote(player, verb, args)
				result.Messages = append(result.Messages, emResult.Messages...)
				result.RoomBroadcast = append(result.RoomBroadcast, emResult.RoomBroadcast...)
				result.TargetName = emResult.TargetName
				result.TargetMsg = emResult.TargetMsg
			}
			return result
		}
	}
	return e.processEmote(player, verb, args)
}

// runVerbScriptsForTarget finds a named target in the room or player inventory and runs all
// verb scripts (root IFVAR, IFPREVERB, IFVERB) on it. Returns nil if the target is not found
// or if the scripts produce no output and do not block the action. Used by doEmoteWithScripts
// so that emote verbs check scripts before falling back to the emote table.
func (e *GameEngine) runVerbScriptsForTarget(ctx context.Context, player *Player, room *gameworld.Room, verb string, target string) *CommandResult {
	if room == nil {
		return nil
	}
	target, skip := parseOrdinal(target)

	// Search room items
	for _, ri := range room.Items {
		itemDef := e.items[ri.Archetype]
		if itemDef == nil {
			continue
		}
		name := e.getItemNounName(itemDef)
		if matchesTarget(name, target, e.getAdjName(ri.Adj1), e.getAdjName(ri.Adj2), e.getAdjName(ri.Adj3)) {
			if skip > 0 { skip--; continue }
			// Copy ri so that script side-effects (e.g. REMOVEITEM -1) that modify
			// room.Items cannot invalidate our pointer mid-loop.
			riCopy := ri
			origRoom := player.RoomNumber // capture before scripts may MOVE the player
			result := &CommandResult{}
			sc0 := e.RunItemScripts(player, room, verb, &riCopy, itemDef)
			sc1 := e.RunPreverbScripts(player, room, verb, &riCopy, itemDef)
			sc2 := e.RunVerbScripts(player, room, verb, &riCopy, itemDef)
			// PLREVENT/CONTPLREVENT-deferred actions must be scheduled, or everything
			// after the delay is lost.
			for _, segs := range [][]ScriptSegment{sc0.DeferredSegments, sc1.DeferredSegments, sc2.DeferredSegments} {
				if len(segs) > 0 {
					e.scheduleScriptSegments(player, segs)
				}
			}
			result.Messages = append(result.Messages, sc0.Messages...)
			result.Messages = append(result.Messages, sc1.Messages...)
			result.Messages = append(result.Messages, sc2.Messages...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc0.RoomMsgs...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc1.RoomMsgs...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc2.RoomMsgs...)
			result.GMBroadcast = append(result.GMBroadcast, sc0.GMMsgs...)
			result.GMBroadcast = append(result.GMBroadcast, sc1.GMMsgs...)
			result.GMBroadcast = append(result.GMBroadcast, sc2.GMMsgs...)
			blocked := sc0.Blocked || sc1.Blocked || sc2.Blocked
			moveTo := sc0.MoveTo
			if sc1.MoveTo > 0 { moveTo = sc1.MoveTo }
			if sc2.MoveTo > 0 { moveTo = sc2.MoveTo }
			if moveTo > 0 {
				dest := e.rooms[moveTo]
				if dest != nil {
					// doMove may have already updated player.RoomNumber; ensure it's set.
					player.RoomNumber = moveTo
					e.SavePlayer(ctx, player)
					lookResult := e.doLook(player)
					result.Messages = append(result.Messages, lookResult.Messages...)
					result.RoomName = lookResult.RoomName
					result.RoomDesc = lookResult.RoomDesc
					result.Exits = lookResult.Exits
					result.Items = lookResult.Items
					result.OldRoom = origRoom
					result.OldRoomMsg = []string{fmt.Sprintf("%s leaves.", player.DisplayNameCap())}
					result.RoomBroadcast = append(result.RoomBroadcast, fmt.Sprintf("%s arrives.", player.DisplayNameCap()))
					e.applyEntryScripts(ctx, player, dest, result)
				}
				return result
			}
			if blocked {
				if len(result.Messages) == 0 {
					result.Messages = []string{"You can't do that."}
				}
				return result
			}
			if len(result.Messages) > 0 || len(result.RoomBroadcast) > 0 {
				return result
			}
			return nil // Item found but no scripts produced output — fall back to emote
		}
	}

	// Search player items (inventory, worn, wielded)
	allPlayerItems := make([]InventoryItem, 0, len(player.Inventory)+len(player.Worn)+1)
	allPlayerItems = append(allPlayerItems, player.Inventory...)
	allPlayerItems = append(allPlayerItems, player.Worn...)
	wieldedIdx := -1
	if player.Wielded != nil {
		wieldedIdx = len(allPlayerItems)
		allPlayerItems = append(allPlayerItems, *player.Wielded)
	}
	for idx, ii := range allPlayerItems {
		itemDef := e.items[ii.Archetype]
		if itemDef == nil {
			continue
		}
		name := e.getItemNounName(itemDef)
		if matchesTarget(name, target, e.getAdjName(ii.Adj1), e.getAdjName(ii.Adj2), e.getAdjName(ii.Adj3)) {
			if skip > 0 { skip--; continue }
			// State drives IFITEM -1 WORN/WIELDED checks (e.g. item 631's flute script).
			itemState := ii.State
			if itemState == "" {
				if idx == wieldedIdx {
					itemState = "WIELDED"
				} else if ii.WornSlot != "" {
					itemState = "WORN"
				}
			}
			tempRI := gameworld.RoomItem{Ref: -1, Archetype: ii.Archetype,
				Adj1: ii.Adj1, Adj2: ii.Adj2, Adj3: ii.Adj3,
				Val1: ii.Val1, Val2: ii.Val2, Val3: ii.Val3, Val4: ii.Val4, Val5: ii.Val5,
				ItemBits: ii.ItemBits,
				State: itemState}
			result := &CommandResult{}
			sc0 := e.RunItemScripts(player, room, verb, &tempRI, itemDef)
			sc1 := e.RunPreverbScripts(player, room, verb, &tempRI, itemDef)
			sc2 := e.RunVerbScripts(player, room, verb, &tempRI, itemDef)
			// PLREVENT/CONTPLREVENT-deferred actions must be scheduled, or everything
			// after the delay is lost.
			for _, segs := range [][]ScriptSegment{sc0.DeferredSegments, sc1.DeferredSegments, sc2.DeferredSegments} {
				if len(segs) > 0 {
					e.scheduleScriptSegments(player, segs)
				}
			}
			result.Messages = append(result.Messages, sc0.Messages...)
			result.Messages = append(result.Messages, sc1.Messages...)
			result.Messages = append(result.Messages, sc2.Messages...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc0.RoomMsgs...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc1.RoomMsgs...)
			result.RoomBroadcast = append(result.RoomBroadcast, sc2.RoomMsgs...)
			blocked := sc0.Blocked || sc1.Blocked || sc2.Blocked
			if blocked {
				if len(result.Messages) == 0 {
					result.Messages = []string{"You can't do that."}
				}
				return result
			}
			if len(result.Messages) > 0 || len(result.RoomBroadcast) > 0 {
				return result
			}
			return nil // Item found but no scripts produced output
		}
	}

	return nil // Target not found — let caller fall back to emote
}

func (e *GameEngine) doWhisper(player *Player, args []string, rawInput string) *CommandResult {
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	if len(args) < 2 {
		return &CommandResult{Messages: []string{"Whisper to whom?"}}
	}
	targetName := strings.ToLower(args[0])

	// Proximity whisper: "whisper close ..." or "whisper those ..."
	if targetName == "close" || targetName == "those" {
		text := extractRawArgs(rawInput, 2)
		if text == "" {
			return &CommandResult{Messages: []string{"Whisper what?"}}
		}
		roomLine := fmt.Sprintf("%s whispers to those close, \"%s\"", player.DisplayNameCap(), text)
		if player.IsConcealed() {
			roomLine = fmt.Sprintf("Something whispers to those close, \"%s\"", text)
		}
		return &CommandResult{
			Messages:      []string{fmt.Sprintf("You whisper to those close, \"%s\"", text)},
			RoomBroadcast: []string{roomLine},
			// roomLine is already anonymized above for a concealed whisperer, so this
			// opts out of api.go's default choke-point suppression.
			ConcealedBroadcastOK: true,
		}
	}

	found := e.findPlayerInRoom(player, targetName)
	if found == nil {
		return &CommandResult{Messages: []string{"You don't see that person here."}}
	}
	// Get the whisper text (everything after the target name)
	text := extractRawArgs(rawInput, 2)
	if text == "" {
		return &CommandResult{Messages: []string{"Whisper what?"}}
	}
	roomLine := fmt.Sprintf("%s whispers to %s.", player.DisplayNameCap(), found.DisplayName())
	if player.IsConcealed() {
		roomLine = fmt.Sprintf("Something whispers to %s.", found.DisplayName())
	}
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You whisper to %s.", found.DisplayName())},
		RoomBroadcast: []string{roomLine},
		TargetName:    found.FirstName, // session routing key — must stay the real name even if found is disguised
		WhisperTarget: found.FirstName,
		WhisperMsg:    fmt.Sprintf("%s whispers to you, \"%s\"", player.DisplayNameCap(), text),
		// roomLine is already anonymized above for a concealed whisperer, so this
		// opts out of api.go's default choke-point suppression.
		ConcealedBroadcastOK: true,
	}
}

// doSayTo handles "SAYTO <player> <message>" — a public, room-wide statement
// addressed to a specific person, unlike WHISPER which is a private aside.
// No IFSAY scripts run here: those blocks are original-content triggers for the
// literal SAY command, and SAYTO has no original precedent (it's a new roleplay
// convenience command), so it mirrors WHISPER/YELL in staying script-free.
func (e *GameEngine) doSayTo(player *Player, args []string, rawInput string) *CommandResult {
	if player.IsSilenced() {
		return &CommandResult{Messages: []string{"You try to speak, but no sound comes out!"}}
	}
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	if len(args) < 2 {
		return &CommandResult{Messages: []string{"Say what to whom?"}}
	}
	targetName := strings.ToLower(args[0])
	found := e.findPlayerInRoom(player, targetName)
	if found == nil {
		return &CommandResult{Messages: []string{"You don't see that person here."}}
	}
	text := extractRawArgs(rawInput, 2)
	if text == "" {
		return &CommandResult{Messages: []string{"Say what to whom?"}}
	}
	roomLine := fmt.Sprintf("%s says to %s, \"%s\"", player.DisplayNameCap(), found.DisplayName(), text)
	if player.IsConcealed() {
		roomLine = fmt.Sprintf("Something says to %s, \"%s\"", found.DisplayName(), text)
	}
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You say to %s, \"%s\"", found.DisplayName(), text)},
		RoomBroadcast: []string{roomLine},
		// roomLine is already anonymized above for a concealed speaker, so this
		// opts out of api.go's default choke-point suppression.
		ConcealedBroadcastOK: true,
	}
}

func (e *GameEngine) doYell(player *Player, args []string, rawInput string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{Messages: []string{"Yell what?"}}
	}
	if player.IsSilenced() {
		return &CommandResult{Messages: []string{"You try to yell, but no sound comes out!"}}
	}
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	text := extractRawArgs(rawInput, 1)
	adverb := ""
	if player.SpeechAdverb != "" && !player.WolfForm {
		adverb = player.SpeechAdverb + " "
	}

	// Yell is heard in adjacent rooms too
	room := e.rooms[player.RoomNumber]
	if room != nil && e.roomBroadcast != nil {
		adjacentMsg := fmt.Sprintf("You hear someone yell, \"%s\"", text)
		for _, destNum := range room.Exits {
			if destNum > 0 && destNum != player.RoomNumber {
				e.roomBroadcast(destNum, []string{adjacentMsg})
			}
		}
	}

	roomLine := fmt.Sprintf("%s %syells, \"%s\"", player.DisplayNameCap(), adverb, text)
	if player.IsConcealed() {
		roomLine = fmt.Sprintf("Something yells, \"%s\"", text)
	}
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You %syell, \"%s\"", adverb, text)},
		RoomBroadcast: []string{roomLine},
		// roomLine is already anonymized above for a concealed yeller, so this
		// opts out of api.go's default choke-point suppression.
		ConcealedBroadcastOK: true,
	}
}

// doPray handles the PRAY command — triggers IFVERB PRAY scripts or generic prayer.
func (e *GameEngine) doPray(player *Player) *CommandResult {
	room := e.rooms[player.RoomNumber]
	if room != nil {
		sc := &ScriptContext{Player: player, Room: room, Engine: e, activeVerb: "PRAY", activeRef: "-1"}
		for _, block := range room.Scripts {
			if block.Type == "IFVERB" && len(block.Args) >= 2 {
				if strings.ToUpper(block.Args[0]) == "PRAY" && block.Args[1] == "-1" {
					sc.execBlock(block)
				}
			}
		}
		if len(sc.Messages) > 0 {
			result := &CommandResult{Messages: sc.Messages}
			if len(sc.RoomMsgs) > 0 {
				result.RoomBroadcast = sc.RoomMsgs
			}
			return result
		}
	}
	pronoun := "his"
	if player.Gender == GenderFemale {
		pronoun = "her"
	}
	return &CommandResult{
		Messages:      []string{"You pray."},
		RoomBroadcast: []string{fmt.Sprintf("%s bows %s head and prays.", player.DisplayName(), pronoun)},
	}
}

// doContact handles the CONTACT command — psionic telepathic whisper.
func (e *GameEngine) doContact(player *Player, args []string, rawInput string) *CommandResult {
	if len(args) < 2 {
		return &CommandResult{Messages: []string{"Contact whom with what message?"}}
	}
	// CONTACT requires psionic ability (Psionics skill 26 or any psionic school skill)
	if player.Skills[26] < 1 && player.Skills[27] < 1 && player.Skills[28] < 1 && player.Skills[29] < 1 {
		return &CommandResult{Messages: []string{"You do not possess psionic abilities."}}
	}
	targetName := strings.ToLower(args[0])
	// Find the target among all online players (not just same room)
	var found *Player
	if e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == player.FirstName && p.LastName == player.LastName {
				continue
			}
			if p.NameMatches(targetName) {
				found = p
				break
			}
		}
	}
	if found == nil {
		return &CommandResult{Messages: []string{"You cannot sense that person."}}
	}
	text := extractRawArgs(rawInput, 2)
	if text == "" {
		return &CommandResult{Messages: []string{"Contact whom with what message?"}}
	}
	contactRT := applyRoundTime(player, 2)
	player.RoundTimeExpiry = time.Now().Add(time.Duration(contactRT) * time.Second)
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You contact %s with your thoughts.", found.DisplayName()), fmt.Sprintf("[Round: %d sec]", contactRT)},
		WhisperTarget: found.FirstName,
		WhisperMsg:    fmt.Sprintf("You feel the touch of %s's mind: \"%s\"", player.FirstName, text),
	}
}

// doFollow handles the FOLLOW command — join a group.
func (e *GameEngine) doFollow(player *Player, args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{Messages: []string{"Follow whom?"}}
	}
	target := strings.ToLower(strings.Join(args, " "))
	found := e.findPlayerInRoom(player, target)
	if found == nil {
		return &CommandResult{Messages: []string{"They are not here."}}
	}
	if player.Following != "" {
		return &CommandResult{Messages: []string{fmt.Sprintf("You are already following %s.", e.displayNameForOnlinePlayer(player.Following))}}
	}
	if found.Following == player.FirstName {
		return &CommandResult{Messages: []string{"You can't follow someone who is following you."}}
	}
	player.Following = found.FirstName
	found.IsGroupLeader = true
	// Add to leader's group members (avoid duplicates)
	alreadyIn := false
	for _, m := range found.GroupMembers {
		if m == player.FirstName {
			alreadyIn = true
			break
		}
	}
	if !alreadyIn {
		found.GroupMembers = append(found.GroupMembers, player.FirstName)
	}
	result := &CommandResult{Messages: []string{fmt.Sprintf("You are now following %s.", found.DisplayName())}}
	if !player.IsConcealed() {
		result.RoomBroadcast = []string{fmt.Sprintf("%s is now following %s.", player.DisplayNameCap(), found.DisplayName())}
	}
	return result
}

// doHold handles the HOLD command (group) — leader adds a member.
func (e *GameEngine) doHold(player *Player, found *Player) *CommandResult {
	if e.isAvoiding(player.FirstName, found) {
		return avoidBlockMessage(found.FirstName)
	}
	if found.Following != "" {
		return &CommandResult{Messages: []string{fmt.Sprintf("%s is already following someone.", found.DisplayName())}}
	}
	found.Following = player.FirstName
	player.IsGroupLeader = true
	alreadyIn := false
	for _, m := range player.GroupMembers {
		if m == found.FirstName {
			alreadyIn = true
			break
		}
	}
	if !alreadyIn {
		player.GroupMembers = append(player.GroupMembers, found.FirstName)
	}
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("%s takes %s by the hand.", player.DisplayName(), found.DisplayName())},
		RoomBroadcast: []string{fmt.Sprintf("%s takes %s by the hand.", player.DisplayName(), found.DisplayName())},
	}
}

// finishPreverbMoveGroup builds the result for a direction/ASCEND/DESCEND
// IFPREVERB that fired MOVEGROUP, then performs the move. sc.RoomMsgs are
// pre-MOVEGROUP departure echoes, so they go to the room being left rather than
// whatever room player.RoomNumber points at after the move.
func (e *GameEngine) finishPreverbMoveGroup(ctx context.Context, player *Player, sc *ScriptContext) *CommandResult {
	result := &CommandResult{}
	result.Messages = append(result.Messages, sc.Messages...)
	result.GMBroadcast = append(result.GMBroadcast, sc.GMMsgs...)
	if len(sc.RoomMsgs) > 0 {
		result.OldRoom = player.RoomNumber
		result.OldRoomMsg = append(result.OldRoomMsg, sc.RoomMsgs...)
	}
	if sc.RoundTimeSet > 0 && !messagesHaveRoundTime(result.Messages) {
		result.Messages = append(result.Messages, fmt.Sprintf("[Round: %d sec]", sc.RoundTimeSet))
	}
	e.moveGroupToRoom(ctx, player.RoomNumber, sc.MoveGroupTo)
	return result
}

// moveGroupToRoom moves all online players in srcRoom to destRoom (for MOVEGROUP script command).
func (e *GameEngine) moveGroupToRoom(ctx context.Context, srcRoom, destRoom int) {
	dest := e.rooms[destRoom]
	if dest == nil || e.sessions == nil {
		return
	}
	// Relocate everyone first, then render looks — otherwise the first player
	// moved would get a look at destRoom before their groupmates had actually
	// arrived, making it look like the rest of the group didn't come along.
	var moved []*Player
	for _, p := range e.sessions.OnlinePlayers() {
		if p.RoomNumber == srcRoom && !p.Dead {
			p.RoomNumber = destRoom
			p.Submitting = false
			e.disengageCombat(p)
			e.SavePlayer(ctx, p)
			moved = append(moved, p)
		}
	}
	for _, p := range moved {
		if e.sendToPlayer != nil {
			lookResult := e.doLook(p)
			e.sendToPlayer(p.FirstName, lookResult.Messages)
		}
		// applyEntryScripts writes into whatever CommandResult it's handed — a bare
		// &CommandResult{} literal here was a throwaway nobody ever read, so any
		// IFENTRY reaction (an ECHO PLAYER, a DAMAGEPLR message, even a second
		// MOVEGROUP bouncing the player straight back out — see EDDIE3.SCR room 76's
		// "your dagger is not the right one" check) was computed and then silently
		// dropped: the player would see themselves plucked right back to where they
		// started with no explanation at all. Capture it and actually deliver it,
		// the same way scheduleScriptSegments already does for its own IFENTRY calls.
		entryResult := &CommandResult{}
		e.applyEntryScripts(ctx, p, dest, entryResult)
		if len(entryResult.Messages) > 0 && e.sendToPlayer != nil {
			e.sendToPlayer(p.FirstName, entryResult.Messages)
		}
		if len(entryResult.RoomBroadcast) > 0 {
			if e.roomBroadcastExclude != nil {
				e.roomBroadcastExclude(p.RoomNumber, p.FirstName, entryResult.RoomBroadcast)
			} else if e.roomBroadcast != nil {
				e.roomBroadcast(p.RoomNumber, entryResult.RoomBroadcast)
			}
		}
		if len(entryResult.GMBroadcast) > 0 && e.gmBroadcast != nil {
			e.gmBroadcast(entryResult.GMBroadcast)
		}
	}
}

// displayNameForOnlinePlayer resolves a stored real FirstName (e.g. from
// Player.Following or Player.GroupMembers, which always track real identity
// so group bookkeeping survives a member re-disguising) to that player's
// current disguise-aware DisplayName, so group messages describe someone by
// the persona they're currently wearing rather than leaking their real name.
// Falls back to the raw name if the player can't be found online.
func (e *GameEngine) findOnlinePlayerByName(name string) *Player {
	if e.sessions == nil {
		return nil
	}
	for _, p := range e.sessions.OnlinePlayers() {
		if p.FirstName == name {
			return p
		}
	}
	return nil
}

func (e *GameEngine) displayNameForOnlinePlayer(name string) string {
	if p := e.findOnlinePlayerByName(name); p != nil {
		return p.DisplayName()
	}
	return name
}

func (e *GameEngine) removeFromGroup(player *Player) {
	if player.Following == "" {
		return
	}
	leaderName := player.Following
	player.Following = ""
	if e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == leaderName {
				for i, m := range p.GroupMembers {
					if m == player.FirstName {
						p.GroupMembers = append(p.GroupMembers[:i], p.GroupMembers[i+1:]...)
						break
					}
				}
				if len(p.GroupMembers) == 0 {
					p.IsGroupLeader = false
				}
				break
			}
		}
	}
}

// followBlockedReason reports why a group member currently can't keep up
// with a moving leader — stuck in round time, sitting, laying down, or
// stunned — or "" if they're free to follow.
func followBlockedReason(p *Player) string {
	if p.RoundTimeExpiry.After(time.Now()) {
		return "still recovering"
	}
	switch p.Position {
	case 1:
		return "sitting down"
	case 2:
		return "laying down"
	}
	if p.Stunned {
		return "stunned"
	}
	return ""
}

// moveGroupFollowers relocates a leader's online group members from
// originalRoom to destNum, for the caller to render along with the leader's
// own move. For an on-foot move (bypassFollowCheck false), a member who is in
// round time, sitting, laying down, or stunned can't keep up — rather than
// dragging them along, they're left behind and dropped from the group
// (mirroring removeFromGroup: their Following is cleared, they come off the
// leader's GroupMembers list, and the leader loses IsGroupLeader if that was
// their last member). A magical mass-teleport (bypassFollowCheck true — e.g.
// Bend Space II) sweeps every group member along regardless of that physical
// state: the spell relocates them, they don't need to walk anywhere to keep
// up. Returns the members who actually came along.
func (e *GameEngine) moveGroupFollowers(ctx context.Context, leader *Player, originalRoom, destNum int, bypassFollowCheck bool) []*Player {
	if !leader.IsGroupLeader || len(leader.GroupMembers) == 0 || e.sessions == nil {
		return nil
	}
	var moved []*Player
	// Snapshot the member list — removeFromGroup mutates leader.GroupMembers
	// in place, which would otherwise skip entries while ranging over it live.
	for _, memberName := range append([]string(nil), leader.GroupMembers...) {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName != memberName || p.RoomNumber != originalRoom || p.Dead {
				continue
			}
			if !bypassFollowCheck {
				if reason := followBlockedReason(p); reason != "" {
					leaderDisplay := leader.DisplayName()
					e.removeFromGroup(p)
					if e.sendToPlayer != nil {
						e.sendToPlayer(p.FirstName, []string{fmt.Sprintf("You are %s and fail to follow %s! You are no longer following them.", reason, leaderDisplay)})
					}
					break
				}
			}
			p.RoomNumber = destNum
			p.Submitting = false
			e.disengageCombat(p)
			e.SavePlayer(ctx, p)
			moved = append(moved, p)
			break
		}
	}
	return moved
}

// doLeave handles the LEAVE command — stop following.
func (e *GameEngine) doLeave(player *Player) *CommandResult {
	if player.Following == "" {
		return &CommandResult{Messages: []string{"You are not following anyone."}}
	}
	leaderName := player.Following
	leaderDisplay := e.displayNameForOnlinePlayer(leaderName)
	e.removeFromGroup(player)
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You stop following %s.", leaderDisplay)},
		RoomBroadcast: []string{fmt.Sprintf("%s stops following %s.", player.DisplayName(), leaderDisplay)},
	}
}

// doDisband handles the DISBAND command — leader disbands their group.
func (e *GameEngine) doDisband(player *Player) *CommandResult {
	if !player.IsGroupLeader || len(player.GroupMembers) == 0 {
		return &CommandResult{Messages: []string{"You don't have a group to disband."}}
	}
	// Clear Following on all members
	if e.sessions != nil {
		for _, memberName := range player.GroupMembers {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.FirstName == memberName {
					p.Following = ""
					if e.sendToPlayer != nil {
						e.sendToPlayer(p.FirstName, []string{fmt.Sprintf("%s disbands the group.", player.DisplayName())})
					}
					break
				}
			}
		}
	}
	player.GroupMembers = nil
	player.IsGroupLeader = false
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You disband your group.")},
		RoomBroadcast: []string{fmt.Sprintf("%s disbands their group.", player.DisplayName())},
	}
}

// doSharedXP handles the SHAREDXP command — only the group leader may toggle it. While
// active, a kill by any member of the group splits XP across the whole group (see
// sharedXPRecipients/computeGroupXPShares in combat.go) instead of going solely to
// whoever landed the kill. Message goes to every current member, not just the leader,
// since the mode affects everyone's XP going forward.
func (e *GameEngine) doSharedXP(player *Player) *CommandResult {
	if !player.IsGroupLeader {
		return &CommandResult{Messages: []string{"Only the leader of a group can toggle shared experience."}}
	}
	player.SharedXP = !player.SharedXP
	msg := "Your group is no longer sharing experience from combat."
	if player.SharedXP {
		msg = "Your group will now share experience from combat."
	}
	if e.sendToPlayer != nil {
		for _, memberName := range player.GroupMembers {
			e.sendToPlayer(memberName, []string{msg})
		}
	}
	return &CommandResult{Messages: []string{msg}}
}

// groupMates returns every other online player currently in player's group — the
// leader and its other followers if player is a follower, or all of player's own
// followers if player is the leader — excluding player itself. Returns nil if player
// isn't in a group. Used by group-wide effects like Mass Protection (spell 130, see
// massProtectionGroupBonus in combat.go).
func (e *GameEngine) groupMates(player *Player) []*Player {
	if e.sessions == nil {
		return nil
	}
	var otherNames []string
	if player.IsGroupLeader {
		if len(player.GroupMembers) == 0 {
			return nil
		}
		otherNames = player.GroupMembers
	} else if player.Following != "" {
		leaderName := player.Following
		var leader *Player
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == leaderName {
				leader = p
				break
			}
		}
		if leader == nil {
			return nil
		}
		otherNames = append([]string{leaderName}, leader.GroupMembers...)
		for i, n := range otherNames {
			if n == player.FirstName {
				otherNames = append(otherNames[:i], otherNames[i+1:]...)
				break
			}
		}
	} else {
		return nil
	}

	var mates []*Player
	for _, name := range otherNames {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == name {
				mates = append(mates, p)
				break
			}
		}
	}
	return mates
}

// doSplit divides a currency amount evenly among all group members.
// The player paying deducts their share for each other member; remainder stays with them.
func (e *GameEngine) doSplit(ctx context.Context, player *Player, args []string) *CommandResult {
	if len(args) < 2 {
		return &CommandResult{Messages: []string{"Split how much of what? Usage: split <amount> <gold|silver|copper|kragenmarks|...>"}}
	}
	amount, err := strconv.Atoi(args[0])
	if err != nil || amount <= 0 {
		return &CommandResult{Messages: []string{"Split how much? Usage: split <amount> <gold|silver|copper>"}}
	}
	currency := strings.ToLower(args[1])
	switch currency {
	case "crown", "crowns":
		currency = "gold"
	case "shilling", "shillings":
		currency = "silver"
	case "penny", "pennies":
		currency = "copper"
	case "gold", "silver", "copper":
		// already normalised
	default:
		// A regional currency (currency.go), e.g. "split 30 kragenmarks".
		if md := e.currencyByName(currency); md != nil {
			currency = md.Name
			break
		}
		return &CommandResult{Messages: []string{"You can split gold, silver, copper, or a regional currency."}}
	}

	if !player.IsGroupLeader && player.Following == "" {
		return &CommandResult{Messages: []string{"You are not in a group."}}
	}

	// Collect names of the other group members
	var otherNames []string
	if player.IsGroupLeader {
		if len(player.GroupMembers) == 0 {
			return &CommandResult{Messages: []string{"You don't have any group members to split with."}}
		}
		otherNames = player.GroupMembers
	} else {
		leaderName := player.Following
		var leader *Player
		if e.sessions != nil {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.FirstName == leaderName {
					leader = p
					break
				}
			}
		}
		if leader == nil {
			return &CommandResult{Messages: []string{"Your group leader is not online."}}
		}
		// others = leader + all of leader's members, minus the current player
		otherNames = append([]string{leaderName}, leader.GroupMembers...)
		for i, n := range otherNames {
			if n == player.FirstName {
				otherNames = append(otherNames[:i], otherNames[i+1:]...)
				break
			}
		}
	}

	// Resolve to online Player pointers
	var recipients []*Player
	if e.sessions != nil {
		for _, name := range otherNames {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.FirstName == name {
					recipients = append(recipients, p)
					break
				}
			}
		}
	}
	if len(recipients) == 0 {
		return &CommandResult{Messages: []string{"None of your group members are online."}}
	}

	groupSize := len(recipients) + 1 // others + self
	share := amount / groupSize
	if share == 0 {
		return &CommandResult{Messages: []string{fmt.Sprintf("That's not enough %s to split %d ways.", currency, groupSize)}}
	}
	totalToGive := share * len(recipients)

	switch currency {
	case "gold":
		if player.Gold < totalToGive {
			return &CommandResult{Messages: []string{fmt.Sprintf("You don't have enough gold to give %d crowns to each of %d members.", share, len(recipients))}}
		}
		player.Gold -= totalToGive
		for _, r := range recipients {
			r.Gold += share
			e.SavePlayer(ctx, r)
		}
	case "silver":
		if player.Silver < totalToGive {
			return &CommandResult{Messages: []string{fmt.Sprintf("You don't have enough silver to give %d shillings to each of %d members.", share, len(recipients))}}
		}
		player.Silver -= totalToGive
		for _, r := range recipients {
			r.Silver += share
			e.SavePlayer(ctx, r)
		}
	case "copper":
		if player.Copper < totalToGive {
			return &CommandResult{Messages: []string{fmt.Sprintf("You don't have enough copper to give %d pennies to each of %d members.", share, len(recipients))}}
		}
		player.Copper -= totalToGive
		for _, r := range recipients {
			r.Copper += share
			e.SavePlayer(ctx, r)
		}
	default:
		md := e.currencyByName(currency)
		if !e.spendFunds(player, md, totalToGive) {
			return &CommandResult{Messages: []string{fmt.Sprintf("You don't have enough %s to give %s to each of %d members.", currencyPlural(md), formatCurrency(md, share), len(recipients))}}
		}
		for _, r := range recipients {
			addFunds(r, md, share)
			e.SavePlayer(ctx, r)
		}
	}
	e.SavePlayer(ctx, player)

	shareDisplay := splitCurrencyDisplay(share, currency)
	totalDisplay := splitCurrencyDisplay(amount, currency)

	if e.sendToPlayer != nil {
		for _, r := range recipients {
			e.sendToPlayer(r.FirstName, []string{fmt.Sprintf("%s splits %s with the group. You receive %s.", player.FirstName, totalDisplay, shareDisplay)})
		}
	}
	return &CommandResult{
		Messages: []string{fmt.Sprintf("You split %s among your group of %d. Each member receives %s.", totalDisplay, groupSize, shareDisplay)},
	}
}

func splitCurrencyDisplay(amount int, currency string) string {
	switch currency {
	case "gold":
		if amount == 1 {
			return "1 gold crown"
		}
		return fmt.Sprintf("%d gold crowns", amount)
	case "silver":
		if amount == 1 {
			return "1 silver shilling"
		}
		return fmt.Sprintf("%d silver shillings", amount)
	case "copper":
		if amount == 1 {
			return "1 copper penny"
		}
		return fmt.Sprintf("%d copper pennies", amount)
	}
	if amount == 1 {
		return fmt.Sprintf("1 %s", currency)
	}
	return fmt.Sprintf("%d %ss", amount, currency) // regional currency: singular name + "s"
}

func (e *GameEngine) doWho(player *Player) *CommandResult {
	var names []string
	if e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.GMHidden {
				continue
			}
			name := p.FirstName
			if p.IsBot {
				name += " [Bot]"
			}
			if p.IsGM && p.GMHat {
				name += " [Host]"
			}
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return &CommandResult{Messages: []string{"No adventurers are in the Realms."}}
	}
	sort.Strings(names)
	// Build 4-column grid, 19-char columns
	var msgs []string
	for i := 0; i < len(names); i += 4 {
		line := ""
		for j := 0; j < 4 && i+j < len(names); j++ {
			line += fmt.Sprintf("%-19s", names[i+j])
		}
		msgs = append(msgs, line)
	}
	msgs = append(msgs, "")
	count := len(names)
	if count == 1 {
		msgs = append(msgs, "There is 1 adventurer in the Realms.")
	} else {
		msgs = append(msgs, fmt.Sprintf("There are %d adventurers in the Realms.", count))
	}
	return &CommandResult{Messages: msgs}
}

// helpTopicNames lists the topics HELP <topic> answers, in the original's own order —
// also the canonical set doHelp's intro text advertises and its "no help on that" error
// echoes back.
var helpTopicNames = []string{"combat", "commanding", "death", "movement", "psionics", "spells", "training", "verbs"}

// helpIntro is the exact text HELP with no argument printed in the original game,
// preserved from a 1996 session capture (original/csanburn/wolf0615.txt) — typo in
// "just try tying what you want to do!" included, since it's what players actually saw.
var helpIntro = []string{
	"You are playing Legends of Future Past(tm)",
	"(C)1991-1995 Inner Circle Technologies, Inc.",
	"",
	"You communicate with the world by typing english sentences. If you want to do something, just try tying what you want to do!  The system understands many verbs. Here are some examples of what you could try:",
	"",
	"NORTH            (go north)",
	"SOUTHWEST        (go southwest)",
	"INVENTORY        (see what you are carrying)",
	"LOOK             (view your surroundings)",
	"GO GATE          (go through a gate)",
	"CLIMB LADDER     (climb a ladder)",
	"LOOK UNDER BED   (look at what is under a bed)",
	"TAKE SWORD       (pick up a sword)",
	"KILL RAT         (kill a rat)",
	"",
	"In your travels, you will encounter not only monsters but other characters being played by people like you!  To say something out loud so someone in the room can hear you, just start your input line with a quote character ('). Example:",
	"",
	"You type: 'Hi everyone!",
	`Other people in room see: Moordread says, "Hi everyone!"`,
	"",
	"Some other important commands to know:",
	"ADVICE -- Get some advice and tips on game play",
	"ASSIST -- Call for a GameMaster's assistance",
	"BRIEF -- Turn on brief description mode",
	"EXPERIENCE -- See your experience and build points",
	"FATIGUE -- See your fatigue, mana and psi",
	"FULL -- Turn on full description mode",
	"HEALTH -- Check your health",
	"REPORT -- Report a bug or comment to the Legends staff",
	"STATUS -- See your statistics, skills, etc.",
	"WHO -- See who else is playing Legends",
	"",
	"Good luck!",
	"",
	"Help is available on the following topics:",
	"",
	"combat",
	"commanding",
	"death",
	"movement",
	"psionics",
	"spells",
	"training",
	"verbs",
	"",
	`To view the help information on any of these topics, simply type "HELP <topic>."`,
}

// helpTopics holds the per-topic command references HELP <topic> prints. Only
// "psionics" survives verbatim from an original session capture (see helpIntro's
// comment) — the rest are reconstructed in the same terse "COMMAND <args>: description"
// style from the commands actually implemented today, since no capture of the other
// seven topics survived. Keep this in sync as commands are added, renamed, or retired —
// stale entries here are worse than none.
var helpTopics = map[string][]string{
	"COMBAT": {
		"Combat related commands:",
		"",
		"ATTACK <target>, KILL <target>:  Attack a monster or hostile player",
		"ADVANCE <target>:                Move to engage a target in combat",
		"RETREAT:                         Disengage and back away from combat",
		"FLEE:                            Attempt to run from combat entirely",
		"TARGET <target>:                 Add a target for a multi-target spell (Chain Lightning, etc.)",
		"GUARD [target]:                  Protect a player, portal, or item from harm",
		"BACKSTAB <target>:               A sneak attack from hiding for extra damage",
		"DISARM <target>:                 Attempt to knock the weapon from an opponent's hand",
		"BERSERK, DEFENSIVE, OFFENSIVE, WARY, NORMAL:  Set your combat stance",
		"NOCK/LOAD <weapon>:              Load a ranged weapon before firing it",
		"SPECIALIZE <weapon type>:        Focus your training on a specific weapon type",
	},
	"COMMANDING": {
		"Commanding related commands:",
		"",
		"FOLLOW <person>:                 Join a group by following its leader",
		"LEAVE:                           Stop following your group's leader",
		"DISBAND:                         (leader) Disband your group, releasing all members",
		"SPLIT <amount> <gold/silver/copper>:  Divide coin evenly among your group",
		"SHAREDXP:                        (leader) Toggle whether your group splits combat experience",
		"COMMAND <creature> <action>:     Direct a summoned or bonded creature",
	},
	"DEATH": {
		"Death related commands:",
		"",
		"SUBMIT:                          Yield in combat, signaling you won't fight back",
		"UNSUBMIT:                        Stop submitting",
		"TEND <person>:                   Attempt to stabilize a dying or badly hurt player",
		"CARRY <person>:                  Pick up and carry an incapacitated or submitting player",
		"RELEASE:                         Let go of the person you're carrying",
		"PUTDOWN:                         Set down the person you're carrying",
	},
	"MOVEMENT": {
		"Movement related commands:",
		"",
		"N, S, E, W, NE, NW, SE, SW, UP, DOWN, OUT:  Move in a compass direction",
		"GO <exit/portal>:                Move through a named exit, door, or portal",
		"CLIMB <object>:                  Climb up or through something",
		"ASCEND, DESCEND:                 Move up or down where flight or levitation applies",
		"FLY:                             Take to the air, if you have the means",
		"LAND:                            Come back down to the ground",
		"HIDE:                            Attempt to conceal yourself",
		"SNEAK <direction>:               Move stealthily without announcing your passing",
		"SEARCH:                          Look for hidden exits, items, or passages",
		"DIG:                             Dig at the ground, if you're carrying a tool for it",
	},
	"PSIONICS": {
		"Psionics related commands:",
		"",
		"PSI [Discipline#]: Show current psi statistics, or ready a discipline",
		"PROJECT <target>:  Direct a psionic discipline at a creature",
	},
	"SPELLS": {
		"Spells related commands:",
		"",
		"CAST <spell> [target]:           Cast a spell you've already prepared",
		"PREPARE <spell>, INVOKE <spell>: Ready a spell for casting",
		"CHANT <words>:                   Speak a ritual phrase a spell or script is listening for",
		"MASTER <spell>:                  Attempt to master a spell you already know",
		"SPELL [number]:                  Show your spellbook, or a spell's details",
	},
	"TRAINING": {
		"Training related commands:",
		"",
		"TRAIN:                           Spend build points on skills, at a guild that teaches them",
		"LEARN <spell/skill>:             Learn a new spell or skill from a book, scroll, or trainer",
		"TEACH <person> <spell/skill>:    Teach a spell or skill you know to someone else",
		"UNLEARN <skill>:                 Give up a skill to reclaim its build points",
	},
	"VERBS": {
		"Verbs related commands:",
		"",
		"The system understands many general-purpose verbs beyond the ones covered under the",
		"other topics. Some of the more common ones:",
		"",
		"GET/TAKE, DROP, PUT <item> IN <container>, GIVE <item> TO <person>",
		"OPEN, CLOSE, LOOK, EXAMINE, READ",
		"EAT, DRINK, WEAR, REMOVE, WIELD, UNWIELD",
		"TOUCH, PUSH, PULL, TURN, RUB, TAP, FLIP",
		"LATCH, UNLATCH, MARK, SKIN, APPRAISE, USE",
	},
}

func (e *GameEngine) doHelp(args []string) *CommandResult {
	if len(args) == 0 {
		return &CommandResult{Messages: helpIntro}
	}
	topic := strings.ToUpper(args[0])
	if msgs, ok := helpTopics[topic]; ok {
		return &CommandResult{Messages: msgs}
	}
	return &CommandResult{Messages: []string{
		fmt.Sprintf("No help is available on \"%s\".", args[0]),
		"Help is available on the following topics: " + strings.Join(helpTopicNames, ", ") + ".",
	}}
}

func (e *GameEngine) doThink(player *Player, rawInput string) *CommandResult {
	text := extractOriginalArgs(rawInput)
	if text == "" {
		return &CommandResult{Messages: []string{"Think what?"}}
	}
	if !player.TelepathyActive {
		return &CommandResult{Messages: []string{"You don't have telepathic ability right now."}}
	}
	return &CommandResult{
		Messages:        []string{"You project your thoughts."},
		TelepathyMsg:    text,
		TelepathySender: player.FirstName,
	}
}

func (e *GameEngine) doCant(player *Player, args []string) *CommandResult {
	if msg := formActionBlockMessage(player); msg != "" {
		return &CommandResult{Messages: []string{msg}}
	}
	if len(args) == 0 {
		return &CommandResult{Messages: []string{"Cant what?"}}
	}
	// Requires Legerdemain (skill 21) rank 6+ or Stealth (skill 5)
	if player.Skills[21] < 6 && player.Skills[5] < 1 && !player.IsGM {
		return &CommandResult{Messages: []string{"You don't know how to speak in cant."}}
	}
	text := strings.Join(args, " ")
	return &CommandResult{
		Messages:      []string{fmt.Sprintf("You cant, \"%s\"", text)},
		RoomBroadcast: []string{fmt.Sprintf("%s mutters something under their breath.", player.DisplayName())},
		CantMsg:       text,
		CantSender:    player.FirstName,
	}
}

func (e *GameEngine) doSpeech(ctx context.Context, player *Player, args []string, rawInput string) *CommandResult {
	if len(args) == 0 {
		if player.SpeechAdverb != "" {
			return &CommandResult{Messages: []string{fmt.Sprintf("Your speech manner is: %s. Use SPEECH CLEAR to remove it.", player.SpeechAdverb)}}
		}
		return &CommandResult{Messages: []string{"You have no speech manner set. Use SPEECH <adverb> to set one (e.g. SPEECH gently)."}}
	}
	adverb := strings.ToLower(args[0])
	if adverb == "clear" || adverb == "none" || adverb == "off" {
		player.SpeechAdverb = ""
		e.SavePlayer(ctx, player)
		return &CommandResult{Messages: []string{"Speech manner cleared."}}
	}
	player.SpeechAdverb = adverb
	e.SavePlayer(ctx, player)
	return &CommandResult{Messages: []string{fmt.Sprintf("You will now %s say things.", adverb)}}
}

func (e *GameEngine) doInfo(player *Player) *CommandResult {
	return &CommandResult{Messages: []string{
		fmt.Sprintf("Name: %s", player.FullName()),
		fmt.Sprintf("Race: %s   Gender: %s   Level: %d", player.RaceName(), genderName(player.Gender), player.Level),
		"",
		fmt.Sprintf("Strength: %-3d   Agility: %-3d   Quickness: %d", player.Strength, player.Agility, player.Quickness),
		fmt.Sprintf("Constitution: %-3d   Perception: %-3d   Willpower: %-3d   Empathy: %d", player.Constitution, player.Perception, player.Willpower, player.Empathy),
		"",
		fmt.Sprintf("Body Points: %d/%d   Fatigue: %d/%d", player.BodyPoints, player.MaxBodyPoints, player.Fatigue, player.MaxFatigue),
		fmt.Sprintf("Mana: %d/%d   Psi: %d/%d", player.Mana, player.MaxMana, player.Psi, player.MaxPsi),
		"",
		fmt.Sprintf("Experience: %d   Build Points: %d", player.Experience, player.Experience/100),
	}}
}

func isSelfOr(isSelf bool, selfText, otherText string) string {
	if isSelf { return selfText }
	return otherText
}

func genderName(g int) string {
	switch g {
	case 0:
		return "Male"
	case 1:
		return "Female"
	default:
		return "Unknown"
	}
}

// doPay triggers IFPREVERB PAY -1 room scripts (altar payments, magical offerings, etc.).
func (e *GameEngine) doPay(ctx context.Context, player *Player) *CommandResult {
	room := e.rooms[player.RoomNumber]
	if room == nil {
		return &CommandResult{Messages: []string{"You can't pay here."}}
	}
	sc := e.RunRoomVerbScripts(player, room, "PAY")
	if sc.NeedsSave {
		e.SavePlayer(ctx, player)
	}
	// PLREVENT/CONTPLREVENT-deferred actions must be scheduled, or everything after
	// the delay is lost — e.g. room 3740's token machine gives the token via a
	// deferred NEWITEM after the "clunk" message, which never fired without this.
	if len(sc.DeferredSegments) > 0 {
		e.scheduleScriptSegments(player, sc.DeferredSegments)
	}
	result := &CommandResult{Messages: sc.Messages, RoomBroadcast: sc.RoomMsgs}
	if sc.MoveTo > 0 {
		e.applySayMove(ctx, player, sc, result)
	}
	if len(result.Messages) == 0 {
		return &CommandResult{Messages: []string{"You can't pay here."}}
	}
	return result
}

// applySayMove executes a script-driven MOVE from an IFSAY or PAY context,
// updating player location and appending look/entry results to the command result.
func (e *GameEngine) applySayMove(ctx context.Context, player *Player, sc *ScriptContext, result *CommandResult) {
	dest := e.rooms[sc.MoveTo]
	if dest == nil {
		return
	}
	// sc.OrigRoomNum is set by doMove before it updates player.RoomNumber; use it
	// so OldRoom is correct even when doMove changed player.RoomNumber immediately.
	origRoom := sc.OrigRoomNum
	if origRoom == 0 {
		origRoom = player.RoomNumber
	}
	// Split the room broadcast at the MOVE. doMove moves everything echoed before the
	// MOVE into sc.PreMoveMsgs and clears sc.RoomMsgs, so sc.RoomMsgs holds only what
	// was echoed after it; the caller built result.RoomBroadcast as the spoken line (SAY;
	// nothing for PAY) followed by sc.RoomMsgs. The speech and pre-move echoes (e.g.
	// "...and she vanishes!") belong to the room being left; post-move echoes (e.g.
	// AFFECT <dest> + "...appears in a swirl of wind") belong to the destination.
	// Previously the speech went to the destination and pre-move echoes were dropped.
	speechLines := result.RoomBroadcast
	if n := len(result.RoomBroadcast) - len(sc.RoomMsgs); n >= 0 {
		speechLines = result.RoomBroadcast[:n]
	}
	postMove := sc.RoomMsgs
	oldRoomMsgs := append(append([]string{}, speechLines...), sc.PreMoveMsgs...)
	if len(sc.PreMoveMsgs) == 0 {
		oldRoomMsgs = append(oldRoomMsgs, fmt.Sprintf("%s leaves.", player.DisplayNameCap()))
	}

	player.RoomNumber = sc.MoveTo
	e.SavePlayer(ctx, player)
	lookResult := e.doLook(player)
	result.Messages = append(result.Messages, lookResult.Messages...)
	result.RoomName = lookResult.RoomName
	result.RoomDesc = lookResult.RoomDesc
	result.Exits = lookResult.Exits
	result.Items = lookResult.Items
	result.OldRoom = origRoom
	result.OldRoomMsg = oldRoomMsgs
	result.RoomBroadcast = append([]string{}, postMove...)
	if len(postMove) == 0 {
		result.RoomBroadcast = append(result.RoomBroadcast, fmt.Sprintf("%s arrives.", player.DisplayNameCap()))
	}
	e.applyEntryScripts(ctx, player, dest, result)
}

// HandlePlayerDisconnect cleans up group state when a player disconnects or logs off.
// If the player is a group leader, the group is disbanded and all members are notified.
// If the player is a group member, they are removed from their leader's group.
func (e *GameEngine) HandlePlayerDisconnect(player *Player) {
	e.dismissSummonedCreature(player)
	e.clearPlayerFromGuards(player.FirstName)

	if player.IsGroupLeader && len(player.GroupMembers) > 0 {
		if e.sessions != nil {
			for _, memberName := range player.GroupMembers {
				for _, p := range e.sessions.OnlinePlayers() {
					if p.FirstName == memberName {
						p.Following = ""
						if e.sendToPlayer != nil {
							e.sendToPlayer(p.FirstName, []string{fmt.Sprintf("%s has left the Realms. The group is disbanded.", player.DisplayName())})
						}
						break
					}
				}
			}
		}
		player.GroupMembers = nil
		player.IsGroupLeader = false
	} else if player.Following != "" {
		leaderName := player.Following
		player.Following = ""
		if e.sessions != nil {
			for _, p := range e.sessions.OnlinePlayers() {
				if p.FirstName == leaderName {
					for i, m := range p.GroupMembers {
						if m == player.FirstName {
							p.GroupMembers = append(p.GroupMembers[:i], p.GroupMembers[i+1:]...)
							break
						}
					}
					if len(p.GroupMembers) == 0 {
						p.IsGroupLeader = false
					}
					if e.sendToPlayer != nil {
						e.sendToPlayer(p.FirstName, []string{fmt.Sprintf("%s has left the Realms and is no longer in your group.", player.DisplayName())})
					}
					break
				}
			}
		}
	}

	// Break any carry relationship this player was part of, either way.
	if player.Carrying != "" && e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == player.Carrying {
				p.CarriedBy = ""
				break
			}
		}
		player.Carrying = ""
	}
	if player.CarriedBy != "" && e.sessions != nil {
		for _, p := range e.sessions.OnlinePlayers() {
			if p.FirstName == player.CarriedBy {
				p.Carrying = ""
				break
			}
		}
		player.CarriedBy = ""
	}
}

// checkPortalGuard checks if any online player in the same room is guarding the portal.
// Returns (blocked, playerMsg, oldRoomMsgs): if blocked the caller should return early;
// if not blocked and oldRoomMsgs is non-empty, the bypass message should be added to OldRoomMsg.
func (e *GameEngine) checkPortalGuard(mover *Player, portalArch int) (blocked bool, playerMsgs []string, oldRoomMsgs []string) {
	if e.sessions == nil {
		return
	}
	for _, g := range e.sessions.OnlinePlayers() {
		if g.FirstName == mover.FirstName || g.RoomNumber != mover.RoomNumber || g.Dead {
			continue
		}
		if !containsInt(g.GuardPortals, portalArch) {
			continue
		}

		cmSkill := mover.Skills[10]
		agiBonus := (mover.Agility - 50) / 10
		quickBonus := (mover.Quickness - 50) / 10
		roll := rand.Intn(100) + cmSkill*5 + agiBonus + quickBonus
		threshold := 50 + g.Skills[10]*5

		hidden := mover.Hidden || mover.Invisible || mover.PhantomForm
		moverName := mover.FirstName
		if hidden {
			moverName = "something"
		}

		if roll >= threshold {
			if e.sendToPlayer != nil {
				e.sendToPlayer(g.FirstName, []string{
					fmt.Sprintf("%s slipped past your guard! [Roll: %d vs %d]", capitalize(moverName), roll, threshold),
				})
			}
			return false,
				[]string{fmt.Sprintf("You slip past %s's guard. [Roll: %d vs %d]", g.FirstName, roll, threshold)},
				[]string{fmt.Sprintf("%s slips past %s's guard!", capitalize(moverName), g.FirstName)}
		}

		if e.sendToPlayer != nil {
			e.sendToPlayer(g.FirstName, []string{
				fmt.Sprintf("You block %s from passing! [Roll: %d vs %d]", moverName, roll, threshold),
			})
		}
		return true,
			[]string{fmt.Sprintf("%s blocks your path! [Roll: %d vs %d]", g.FirstName, roll, threshold)},
			[]string{fmt.Sprintf("%s tried to maneuver past %s's guard but was blocked!", capitalize(moverName), g.FirstName)}
	}
	return false, nil, nil
}

// checkItemGuard checks if any online player in the same room is guarding the item archetype.
// On success (bypass), playerMsgs contains the bypass notification for the mover and roomMsgs
// the room echo. On failure (blocked), playerMsgs is the block message to the mover and roomMsgs
// the room echo. Roll info is included in player/guard messages but not room broadcasts.
func (e *GameEngine) checkItemGuard(mover *Player, itemArch int, itemName string) (blocked bool, playerMsgs []string, roomMsgs []string) {
	if e.sessions == nil {
		return
	}
	for _, g := range e.sessions.OnlinePlayers() {
		if g.FirstName == mover.FirstName || g.RoomNumber != mover.RoomNumber || g.Dead {
			continue
		}
		if !containsInt(g.GuardItems, itemArch) {
			continue
		}

		cmSkill := mover.Skills[10]
		agiBonus := (mover.Agility - 50) / 10
		quickBonus := (mover.Quickness - 50) / 10
		roll := rand.Intn(100) + cmSkill*5 + agiBonus + quickBonus
		threshold := 50 + g.Skills[10]*5

		hidden := mover.Hidden || mover.Invisible || mover.PhantomForm
		moverName := mover.FirstName
		if hidden {
			moverName = "something"
		}

		if roll >= threshold {
			if e.sendToPlayer != nil {
				e.sendToPlayer(g.FirstName, []string{
					fmt.Sprintf("%s slipped past your guard and took %s! [Roll: %d vs %d]", capitalize(moverName), itemName, roll, threshold),
				})
			}
			return false,
				[]string{fmt.Sprintf("You slip past %s's guard. [Roll: %d vs %d]", g.FirstName, roll, threshold)},
				[]string{fmt.Sprintf("%s slips past %s's guard and takes %s!", capitalize(moverName), g.FirstName, itemName)}
		}

		if e.sendToPlayer != nil {
			e.sendToPlayer(g.FirstName, []string{
				fmt.Sprintf("You stop %s from taking %s! [Roll: %d vs %d]", moverName, itemName, roll, threshold),
			})
		}
		return true,
			[]string{fmt.Sprintf("%s steps in front of you, preventing you from taking %s. [Roll: %d vs %d]", g.FirstName, itemName, roll, threshold)},
			[]string{fmt.Sprintf("%s tried to take %s but %s blocked them!", capitalize(moverName), itemName, g.FirstName)}
	}
	return false, nil, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
